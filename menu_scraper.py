import os
import re
import random
import threading
import unicodedata
import base64
import json
import time
import argparse
import httpx
import requests
import psycopg2
import psycopg2.extras
from bs4 import BeautifulSoup
from concurrent.futures import ThreadPoolExecutor, as_completed
from dotenv import load_dotenv
from openai import OpenAI
from rapidfuzz import fuzz

load_dotenv(os.path.join(os.path.dirname(__file__), '.env'))

client = OpenAI(
  base_url="https://inference-api.nvidia.com/v1/",
  api_key=os.environ["NVIDIA_Inference_Key"]
)

DSN = os.environ.get("DATABASE_URL", "postgres://postgres:password@localhost:5432/nagoya")
MODEL = "gcp/google/gemini-3-flash-preview"
MAX_PHOTOS = 20
DELAY = 1.5
VLM_WORKERS = 8
MAX_RETRIES = 3  # stop retrying a restaurant after this many errors

HEADERS = {
  "User-Agent": "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36"
}

EXTRACT_PROMPT = (
  "This is a Japanese restaurant menu photo. "
  "Extract all menu items and return as a JSON array. "
  "Each item: name (Japanese as-is), price (integer yen — use glass/per-drink price if both glass and bottle prices exist), "
  "price_bottle (integer yen, only if bottle price also exists, else null), "
  "description (category or tasting note if visible, else null). "
  "Return only the JSON array, no explanation."
)

_vlm_semaphore = threading.Semaphore(VLM_WORKERS)
_print_lock = threading.Lock()


def tprint(*args, **kwargs):
  with _print_lock:
    print(*args, **kwargs)


# ── DB helpers ────────────────────────────────────────────────────────────────

def ensure_tables(conn):
  with conn.cursor() as cur:
    cur.execute("CREATE EXTENSION IF NOT EXISTS pg_trgm")
    cur.execute("""
      CREATE TABLE IF NOT EXISTS menu_items (
        id            BIGSERIAL PRIMARY KEY,
        restaurant_id BIGINT NOT NULL REFERENCES restaurants(id) ON DELETE CASCADE,
        name          TEXT NOT NULL,
        price         INTEGER,
        price_bottle  INTEGER,
        description   TEXT,
        scraped_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
      )
    """)
    cur.execute("""
      CREATE INDEX IF NOT EXISTS menu_items_restaurant_id_idx
      ON menu_items(restaurant_id)
    """)
    cur.execute("""
      CREATE INDEX IF NOT EXISTS menu_items_name_trgm_idx
      ON menu_items USING GIN (name gin_trgm_ops)
    """)
    cur.execute("""
      CREATE TABLE IF NOT EXISTS menu_scrape_log (
        restaurant_id BIGINT PRIMARY KEY REFERENCES restaurants(id) ON DELETE CASCADE,
        status        TEXT NOT NULL,
        error_msg     TEXT,
        retry_count   INTEGER NOT NULL DEFAULT 0,
        attempted_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
        completed_at  TIMESTAMPTZ
      )
    """)
  conn.commit()


def get_pending_restaurants(conn, retry_errors=False):
  """Return list of (id, url) not yet successfully scraped."""
  with conn.cursor() as cur:
    if retry_errors:
      cur.execute("""
        SELECT r.id, r.url
        FROM restaurants r
        LEFT JOIN menu_scrape_log l ON l.restaurant_id = r.id
        WHERE l.status IS NULL
           OR (l.status = 'error' AND l.retry_count < %s)
        ORDER BY r.id
      """, (MAX_RETRIES,))
    else:
      cur.execute("""
        SELECT r.id, r.url
        FROM restaurants r
        LEFT JOIN menu_scrape_log l ON l.restaurant_id = r.id
        WHERE l.status IS NULL
        ORDER BY r.id
      """)
    return cur.fetchall()


def record_log(conn, restaurant_id, status, error_msg=None):
  with conn.cursor() as cur:
    cur.execute("""
      INSERT INTO menu_scrape_log (restaurant_id, status, error_msg, attempted_at, completed_at)
      VALUES (%s, %s, %s, NOW(), CASE WHEN %s = 'done' OR %s = 'no_menu' THEN NOW() ELSE NULL END)
      ON CONFLICT (restaurant_id) DO UPDATE SET
        status       = EXCLUDED.status,
        error_msg    = EXCLUDED.error_msg,
        retry_count  = menu_scrape_log.retry_count + 1,
        attempted_at = NOW(),
        completed_at = EXCLUDED.completed_at
    """, (restaurant_id, status, error_msg, status, status))
  conn.commit()


def save_menu_items(conn, restaurant_id, items):
  with conn.cursor() as cur:
    cur.execute("DELETE FROM menu_items WHERE restaurant_id = %s", (restaurant_id,))
    if items:
      psycopg2.extras.execute_values(
        cur,
        """INSERT INTO menu_items (restaurant_id, name, price, price_bottle, description)
           VALUES %s""",
        [(restaurant_id,
          normalize(item["name"]),
          item.get("price"),
          item.get("price_bottle"),
          item.get("description"))
         for item in items]
      )
  conn.commit()


# ── Scraping helpers ───────────────────────────────────────────────────────────

def normalize(name):
  return unicodedata.normalize("NFKC", name).strip()


def call_vlm(messages, max_retries=6):
  last_exc = RuntimeError("no attempts made")
  with _vlm_semaphore:
    for attempt in range(max_retries):
      try:
        return client.chat.completions.create(
          model=MODEL,
          messages=messages,
          temperature=0.2,
          top_p=0.7,
          max_tokens=8192,
          stream=False
        )
      except Exception as e:
        last_exc = e
        err = str(e)
        is_rate_limit = "429" in err or "rate" in err.lower() or "quota" in err.lower()
        if is_rate_limit and attempt < max_retries - 1:
          wait = (2 ** attempt) + random.uniform(0, 1)
          tprint(f"  Rate limited (attempt {attempt+1}), retrying in {wait:.1f}s...")
          time.sleep(wait)
        else:
          raise
  raise last_exc


def fetch_html(url):
  time.sleep(DELAY)
  r = requests.get(url, headers=HEADERS, timeout=15)
  return r if r.status_code == 200 else None


def extract_html_menu_items(url):
  r = fetch_html(url)
  if not r:
    return []
  soup = BeautifulSoup(r.text, "html.parser")
  items = []
  for item in soup.select(".js-menu-photo-list-target, .c-menu-parts__item, .menu-item"):
    name_el = item.select_one(".c-mv-tl, .menu-item__name")
    price_el = item.select_one(".c-mv-tl-price, .menu-item__price")
    desc_el = item.select_one(".c-mv-tl__description, .menu-item__description")
    if name_el:
      items.append({
        "name": name_el.get_text(strip=True),
        "price": price_el.get_text(strip=True) if price_el else None,
        "description": desc_el.get_text(strip=True) if desc_el else None,
      })
  return items


def parse_json_response(raw, label=""):
  match = re.search(r'\[.*\]', raw, re.DOTALL)
  if match:
    try:
      return json.loads(match.group())
    except json.JSONDecodeError:
      pass
  objects = re.findall(r'\{[^{}]*\}', raw, re.DOTALL)
  if objects:
    items = []
    for obj in objects:
      try:
        items.append(json.loads(obj))
      except json.JSONDecodeError:
        pass
    if items:
      tprint(f"  Note: truncated response for {label}, recovered {len(items)} partial items")
      return items
  if raw:
    tprint(f"  Warning: could not parse JSON{' for ' + label if label else ''}")
  return []


def was_truncated(raw):
  match = re.search(r'\[.*\]', raw, re.DOTALL)
  if match:
    try:
      json.loads(match.group())
      return False
    except json.JSONDecodeError:
      pass
  return True


def fetch_photo_urls(base_url):
  photo_urls = []
  page = 1
  while len(photo_urls) < MAX_PHOTOS:
    url = f"{base_url}?PG={page}" if page > 1 else base_url
    r = fetch_html(url)
    if not r:
      break
    soup = BeautifulSoup(r.text, "html.parser")
    found = 0
    for a in soup.select("a.rstdtl-photo-list__target"):
      src = str(a.get("href") or "")
      if "tblg.k-img.com" not in src:
        continue
      if src not in photo_urls:
        photo_urls.append(src)
        found += 1
        if len(photo_urls) >= MAX_PHOTOS:
          break
    if found == 0:
      break
    if not soup.select_one("a.c-pagination__arrow--next[rel='next']"):
      break
    page += 1
  return photo_urls


def fetch_image_with_retry(url, retries=3):
  last_exc = RuntimeError("no attempts made")
  for attempt in range(retries):
    try:
      return httpx.get(url, timeout=15).content
    except (httpx.ConnectError, httpx.TimeoutException) as e:
      last_exc = e
      if attempt < retries - 1:
        time.sleep(2 ** attempt)
  raise last_exc


def extract_items_from_photo(image_url, index=None, total=None, max_continuations=3):
  label = f"[{index}/{total}]" if index is not None else ""
  try:
    image_data = base64.standard_b64encode(fetch_image_with_retry(image_url)).decode("utf-8")
    image_content = {"type": "image_url", "image_url": {"url": f"data:image/jpeg;base64,{image_data}"}}
    all_items = []
    messages = [{"role": "user", "content": [image_content, {"type": "text", "text": EXTRACT_PROMPT}]}]
    for _ in range(1 + max_continuations):
      completion = call_vlm(messages)
      raw = (completion.choices[0].message.content or "").strip()
      items = parse_json_response(raw, label=image_url)
      all_items.extend(items)
      if not was_truncated(raw) or not items:
        break
      last_name = items[-1].get("name", "")
      tprint(f"  {label} Truncated after {len(items)} items, continuing from '{last_name}'...")
      messages.append({"role": "assistant", "content": raw})
      messages.append({"role": "user", "content": [
        image_content,
        {"type": "text", "text": (
          f"The previous response was cut off. The last item extracted was \"{last_name}\". "
          "Continue extracting the remaining menu items visible in this image that come AFTER that item. "
          "Return only the JSON array of remaining items, no explanation."
        )}
      ]})
      time.sleep(0.5)
    tprint(f"  {label} → {len(all_items)} items extracted")
    return all_items
  except Exception as e:
    tprint(f"  {label} Skipping photo (error: {type(e).__name__}: {str(e)[:80]})")
    return []


def merge_items(all_items):
  if not all_items:
    return []

  def non_null_count(item):
    return sum(v is not None for v in item.values())

  def better(a, b):
    return non_null_count(a) > non_null_count(b)

  seen = {}
  for item in all_items:
    name = normalize(item.get("name", ""))
    if not name:
      continue
    if name not in seen or better(item, seen[name]):
      seen[name] = item

  candidates = list(seen.values())
  THRESHOLD = 88
  merged = []
  used = [False] * len(candidates)
  for i, item in enumerate(candidates):
    if used[i]:
      continue
    best = item
    for j in range(i + 1, len(candidates)):
      if used[j]:
        continue
      score = fuzz.ratio(normalize(item.get("name", "")), normalize(candidates[j].get("name", "")))
      if score >= THRESHOLD:
        used[j] = True
        if better(candidates[j], best):
          best = candidates[j]
    merged.append(best)
  return merged


def scrape_menu(restaurant_url):
  url = restaurant_url.rstrip("/") + "/"

  for suffix in ["dtlmenu/", "dtlmenu/drink/"]:
    items = extract_html_menu_items(url + suffix)
    if items:
      return items

  photo_url = url + "dtlmenu/photo/"
  photos = fetch_photo_urls(photo_url)
  if not photos:
    return None  # signals no_menu

  all_items = []
  total = len(photos)
  with ThreadPoolExecutor(max_workers=VLM_WORKERS) as executor:
    futures = {
      executor.submit(extract_items_from_photo, img_url, i, total): i
      for i, img_url in enumerate(photos, 1)
    }
    for future in as_completed(futures):
      all_items.extend(future.result())

  return merge_items(all_items)


# ── Main ───────────────────────────────────────────────────────────────────────

def main():
  parser = argparse.ArgumentParser(description="Scrape menu items for all restaurants in DB")
  parser.add_argument("--retry-errors", action="store_true",
                      help="retry restaurants that previously failed (up to MAX_RETRIES times)")
  parser.add_argument("--offset", type=int, default=0,
                      help="skip the first N pending restaurants (for splitting work across machines)")
  parser.add_argument("--limit", type=int, default=0,
                      help="stop after processing this many restaurants (0 = no limit)")
  args = parser.parse_args()

  conn = psycopg2.connect(DSN)
  ensure_tables(conn)

  pending = get_pending_restaurants(conn, retry_errors=args.retry_errors)
  total = len(pending)
  print(f"Pending restaurants: {total}")

  if args.offset:
    pending = pending[args.offset:]
    print(f"Skipping first {args.offset} (starting at index {args.offset})")

  if args.limit:
    pending = pending[:args.limit]
    print(f"Limiting to {args.limit}")

  done, skipped_no_menu, failed = 0, 0, 0

  for i, (rst_id, url) in enumerate(pending, 1):
    url = url.replace("/tw/", "/")
    print(f"\n[{i}/{len(pending)}] {url}")
    t_start = time.time()

    try:
      items = scrape_menu(url)
    except Exception as e:
      print(f"  ERROR: {type(e).__name__}: {str(e)[:120]}")
      record_log(conn, rst_id, "error", str(e)[:500])
      failed += 1
      continue

    elapsed = time.time() - t_start

    if items is None:
      print(f"  No menu found ({elapsed:.1f}s)")
      record_log(conn, rst_id, "no_menu")
      skipped_no_menu += 1
      continue

    save_menu_items(conn, rst_id, items)
    record_log(conn, rst_id, "done")
    print(f"  Saved {len(items)} items ({elapsed:.1f}s)")
    done += 1

  conn.close()
  print(f"\nFinished. done={done}, no_menu={skipped_no_menu}, failed={failed}")


if __name__ == "__main__":
  main()
