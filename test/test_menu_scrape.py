import os
import re
import random
import threading
import unicodedata
import base64
import json
import time
import httpx
import requests
from bs4 import BeautifulSoup
from concurrent.futures import ThreadPoolExecutor, as_completed
from dotenv import load_dotenv
from openai import OpenAI
from rapidfuzz import fuzz

load_dotenv(os.path.join(os.path.dirname(__file__), '..', '.env'))

client = OpenAI(
  base_url="https://inference-api.nvidia.com/v1/",
  api_key=os.environ["NVIDIA_Inference_Key"]
)

MODEL = "gcp/google/gemini-3-flash-preview"
MAX_PHOTOS = 20
DELAY = 1.5       # seconds between Tabelog HTML requests
VLM_WORKERS = 8   # concurrent VLM calls per restaurant

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


def call_vlm(messages, max_retries=6):
  """Call VLM API with semaphore throttle and 429 exponential backoff."""
  last_exc: Exception = RuntimeError("no attempts made")
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
        "source": "html"
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
    tprint(f"  Raw response preview: {raw[:150]}")
  return []


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

    tprint(f"  Page {page}: found {found} photos (total so far: {len(photo_urls)})")

    if found == 0:
      break

    next_link = soup.select_one("a.c-pagination__arrow--next[rel='next']")
    if not next_link:
      break
    page += 1

  return photo_urls


def fetch_image_with_retry(url, retries=3) -> bytes:
  last_exc: Exception = RuntimeError("no attempts made")
  for attempt in range(retries):
    try:
      return httpx.get(url, timeout=15).content
    except (httpx.ConnectError, httpx.TimeoutException) as e:
      last_exc = e
      if attempt < retries - 1:
        tprint(f"  Network error, retrying ({attempt+1}/{retries})...")
        time.sleep(2 ** attempt)
  raise last_exc


def was_truncated(raw):
  match = re.search(r'\[.*\]', raw, re.DOTALL)
  if match:
    try:
      json.loads(match.group())
      return False
    except json.JSONDecodeError:
      pass
  return True


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


def normalize(name):
  return unicodedata.normalize("NFKC", name).strip()


def merge_items(all_items):
  if not all_items:
    return []

  def non_null_count(item):
    return sum(v is not None for v in item.values())

  def better(a, b):
    return non_null_count(a) > non_null_count(b)

  seen: dict[str, dict] = {}
  for item in all_items:
    name = normalize(item.get("name", ""))
    if not name:
      continue
    if name not in seen or better(item, seen[name]):
      seen[name] = item

  candidates = list(seen.values())
  print(f"  After exact dedup: {len(candidates)} items (was {len(all_items)})")

  THRESHOLD = 88
  merged: list[dict] = []
  used = [False] * len(candidates)

  for i, item in enumerate(candidates):
    if used[i]:
      continue
    best = item
    for j in range(i + 1, len(candidates)):
      if used[j]:
        continue
      score = fuzz.ratio(
        normalize(item.get("name", "")),
        normalize(candidates[j].get("name", ""))
      )
      if score >= THRESHOLD:
        used[j] = True
        if better(candidates[j], best):
          best = candidates[j]
    merged.append(best)

  print(f"  After fuzzy dedup: {len(merged)} items (threshold={THRESHOLD}%)")
  return merged


def scrape_menu(restaurant_url):
  url = restaurant_url.rstrip("/") + "/"
  print(f"\nRestaurant: {url}")

  # 1. Try HTML menu pages first
  for suffix in ["dtlmenu/", "dtlmenu/drink/"]:
    menu_url = url + suffix
    print(f"\n[HTML] Trying {menu_url}")
    items = extract_html_menu_items(menu_url)
    if items:
      print(f"  Found {len(items)} HTML items — skipping photos")
      return items

  print("\n[Photos] No HTML items found — falling back to photo extraction")

  # 2. Collect photo URLs
  photo_url = url + "dtlmenu/photo/"
  print(f"  Fetching photo list from {photo_url}")
  photos = fetch_photo_urls(photo_url)
  print(f"  Total photos to process: {len(photos)}")

  if not photos:
    print("  No photos found")
    return []

  # 3. Extract from each photo concurrently
  all_items = []
  total = len(photos)
  with ThreadPoolExecutor(max_workers=VLM_WORKERS) as executor:
    futures = {
      executor.submit(extract_items_from_photo, img_url, i, total): i
      for i, img_url in enumerate(photos, 1)
    }
    for future in as_completed(futures):
      all_items.extend(future.result())

  print(f"\n  Total raw items before merge: {len(all_items)}")

  # 4. Merge and deduplicate
  print("  Merging and deduplicating...")
  merged = merge_items(all_items)
  print(f"  Final item count after merge: {len(merged)}")
  return merged


if __name__ == "__main__":
  import csv

  csv_path = os.path.join(os.path.dirname(__file__), '..', 'NagoyaRstUrls.csv')
  with open(csv_path) as f:
    urls = [row['url'] for row in csv.DictReader(f)]

  test_urls = [u.replace("/tw/", "/") for u in urls[:6]]
  summary = []

  for url in test_urls:
    t_start = time.time()
    items = scrape_menu(url)
    elapsed = time.time() - t_start

    summary.append({
      "url": url,
      "items": len(items),
      "time_sec": round(elapsed, 1)
    })

    print("\n" + "="*60)
    print(f"MENU ITEMS ({len(items)} total)")
    print("="*60)
    print(json.dumps(items, ensure_ascii=False, indent=2))

  print("\n" + "="*60)
  print("SUMMARY")
  print("="*60)
  for s in summary:
    print(f"  {s['time_sec']:6.1f}s  {s['items']:3d} items  {s['url']}")
