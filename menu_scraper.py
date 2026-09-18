import os
import re
import random
import threading
import unicodedata
import base64
import json
import time
import argparse
from io import BytesIO
from urllib.parse import urlsplit, urlunsplit
import httpx
import requests
import psycopg2
import psycopg2.extras
from bs4 import BeautifulSoup
from concurrent.futures import ThreadPoolExecutor, as_completed
from dotenv import load_dotenv
from openai import OpenAI
from PIL import Image, ImageOps, UnidentifiedImageError

load_dotenv(os.path.join(os.path.dirname(__file__), '.env'))

PROJECT_ROOT = os.path.dirname(__file__)
FOOD_TAXONOMY_PATH = os.path.join(PROJECT_ROOT, "internal", "db", "food_taxonomy.json")
DSN = os.environ.get("DATABASE_URL", "postgres://postgres:password@localhost:5432/nagoya")
MISTRAL_OCR_URL = "https://api.mistral.ai/v1/ocr"
MISTRAL_OCR_MODEL = "mistral-ocr-4-0"
NVIDIA_BASE_URL = "https://inference-api.nvidia.com/v1"
VLM_MODEL = "gcp/google/gemini-3.6-flash"
MAX_PHOTOS = max(1, int(os.environ.get("MENU_MAX_PHOTOS", "20")))
DELAY = 1.5
OCR_WORKERS = 4
VLM_WORKERS = 8
MAX_RETRIES = 3  # stop retrying a restaurant after this many errors
OCR_TILE_GRID = max(1, int(os.environ.get("MENU_OCR_TILE_GRID", "2")))
OCR_TILE_OVERLAP = 0.08

HEADERS = {
  "User-Agent": "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36"
}

with open(FOOD_TAXONOMY_PATH, encoding="utf-8") as taxonomy_file:
  FOOD_TAXONOMY = json.load(taxonomy_file)

FOOD_TYPE_BY_SLUG = {item["slug"]: item for item in FOOD_TAXONOMY}


def validate_food_taxonomy():
  if len(FOOD_TYPE_BY_SLUG) != len(FOOD_TAXONOMY):
    raise ValueError("Food taxonomy contains duplicate slugs")
  for item in FOOD_TAXONOMY:
    slug = item.get("slug") or ""
    parent_slug = item.get("parent_slug") or ""
    if not slug:
      raise ValueError("Food taxonomy contains an empty slug")
    if parent_slug == slug:
      raise ValueError(f"Food type {slug!r} cannot be its own parent")
    if parent_slug and parent_slug not in FOOD_TYPE_BY_SLUG:
      raise ValueError(f"Food type {slug!r} has unknown parent {parent_slug!r}")

  for item in FOOD_TAXONOMY:
    seen = set()
    slug = item["slug"]
    while slug:
      if slug in seen:
        raise ValueError(f"Food taxonomy contains a parent cycle at {slug!r}")
      seen.add(slug)
      slug = FOOD_TYPE_BY_SLUG[slug].get("parent_slug") or ""


validate_food_taxonomy()
FOOD_PARENT_BY_SLUG = {
  item["slug"]: item.get("parent_slug") or ""
  for item in FOOD_TAXONOMY
}


def food_type_ancestors(slug):
  ancestors = []
  parent = FOOD_PARENT_BY_SLUG.get(slug, "")
  while parent:
    ancestors.append(parent)
    parent = FOOD_PARENT_BY_SLUG.get(parent, "")
  return ancestors


def food_type_prompt_label(item):
  parent_slug = item.get("parent_slug") or ""
  if not parent_slug:
    return item["name_en"]
  return f"{FOOD_TYPE_BY_SLUG[parent_slug]['name_en']} > {item['name_en']}"


FOOD_TAXONOMY_PROMPT = "\n".join(
  f"- {item['slug']}: {food_type_prompt_label(item)} / {item['name_ja']}"
  f" — {item['description']}"
  for item in FOOD_TAXONOMY
)

CATEGORY_PROMPT = """Classify the restaurant's menu evidence into searchable food types.
Use only food_type identifiers from the controlled taxonomy below. Return every directly
supported food type, but do not return drinks, ingredients by themselves, restaurant cuisine,
prices, or headings. The taxonomy is hierarchical. Return the most specific supported child.
Use a parent category only as a fallback when the evidence does not support any listed child.
Never return both an ancestor and its descendant for the same restaurant; searching an ancestor
automatically includes restaurants tagged with its descendants.

For every category, cite one or more exact menu phrases copied from the supplied evidence and
the block containing each phrase. Never repair or invent evidence text. A human will review the
restaurant-level result.

Return only a JSON array using this schema:
[
  {{
    "food_type": "salad",
    "evidence": [{{"text": "野菜サラダ", "block_id": "p0_b0"}}]
  }}
]

Controlled taxonomy:
{taxonomy}

Menu evidence:
{ocr_blocks}
"""

ITEM_NAME_PROMPT = """You are generating menu-item candidates for mandatory human review.
The image and Mistral OCR blocks are provided together.

Favor recall: extract every plausible purchasable food or dish name, including names whose
OCR spelling may be imperfect. Ignore every drink or beverage item, including alcohol,
cocktails, tea, coffee, and soft drinks. A human reviewer will correct or reject every candidate.
Exclude prices, descriptions, category headings, restaurant information, hours, notices,
serving instructions, and text that clearly is not a purchasable food dish.

Copy each candidate exactly from the OCR evidence. Never repair, complete, translate, or
invent text. Omit list-marker punctuation and volume/price suffixes from the name. Do not
omit a plausible item merely because the copied OCR spelling looks wrong. Never return
serving-method options listed under instructions such as "飲み方をお選び下さい".

Return only a JSON array using this schema:
[{{"name":"Japanese item name as written","evidence_block_ids":["p0_b0"]}}]

Every item must cite at least one supplied block containing that exact name. If no item name
is explicitly supported, return [].

OCR blocks:
{ocr_blocks}
"""

_nvidia_client = None
_vlm_access_validated = False
_vlm_access_lock = threading.Lock()
_ocr_semaphore = threading.Semaphore(OCR_WORKERS)
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
    cur.execute("""
      CREATE TABLE IF NOT EXISTS menu_review_tasks (
        id              BIGSERIAL PRIMARY KEY,
        restaurant_id   BIGINT NOT NULL REFERENCES restaurants(id) ON DELETE CASCADE,
        image_url       TEXT NOT NULL,
        source_page_url TEXT NOT NULL DEFAULT '',
        status          TEXT NOT NULL DEFAULT 'pending'
                        CHECK (status IN ('pending', 'completed')),
        reviewer_note   TEXT,
        created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
        reviewed_at     TIMESTAMPTZ,
        UNIQUE (restaurant_id, image_url)
      )
    """)
    cur.execute("""
      CREATE INDEX IF NOT EXISTS menu_review_tasks_status_id_idx
      ON menu_review_tasks(status, id)
    """)
    cur.execute("""
      CREATE TABLE IF NOT EXISTS menu_review_items (
        id                 BIGSERIAL PRIMARY KEY,
        review_task_id     BIGINT NOT NULL REFERENCES menu_review_tasks(id) ON DELETE CASCADE,
        extracted_name     TEXT NOT NULL,
        reviewed_name      TEXT,
        decision           TEXT NOT NULL DEFAULT 'pending'
                           CHECK (decision IN ('pending', 'approved', 'rejected', 'edited')),
        evidence_block_ids JSONB NOT NULL DEFAULT '[]'::JSONB,
        created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
        reviewed_at        TIMESTAMPTZ
      )
    """)
    cur.execute("""
      CREATE INDEX IF NOT EXISTS menu_review_items_task_id_idx
      ON menu_review_items(review_task_id, id)
    """)
    cur.execute("""
      ALTER TABLE menu_items ADD COLUMN IF NOT EXISTS review_item_id BIGINT
      REFERENCES menu_review_items(id) ON DELETE SET NULL
    """)
    cur.execute("""
      CREATE UNIQUE INDEX IF NOT EXISTS menu_items_review_item_id_idx
      ON menu_items(review_item_id) WHERE review_item_id IS NOT NULL
    """)
    cur.execute("""
      CREATE TABLE IF NOT EXISTS food_types (
        id          BIGSERIAL PRIMARY KEY,
        slug        TEXT UNIQUE NOT NULL,
        name_en     TEXT NOT NULL,
        name_ja     TEXT NOT NULL,
        name_zh     TEXT NOT NULL,
        aliases     JSONB NOT NULL DEFAULT '[]'::JSONB,
        description TEXT NOT NULL DEFAULT '',
        parent_id   BIGINT REFERENCES food_types(id) ON DELETE SET NULL,
        active      BOOLEAN NOT NULL DEFAULT TRUE,
        created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
        updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
      )
    """)
    cur.execute("""
      CREATE INDEX IF NOT EXISTS food_types_parent_idx
      ON food_types(parent_id, id)
    """)
    for food_type in FOOD_TAXONOMY:
      cur.execute("""
        INSERT INTO food_types (slug, name_en, name_ja, name_zh, aliases, description)
        VALUES (%s, %s, %s, %s, %s, %s)
        ON CONFLICT (slug) DO UPDATE SET
          name_en = EXCLUDED.name_en,
          name_ja = EXCLUDED.name_ja,
          name_zh = EXCLUDED.name_zh,
          aliases = EXCLUDED.aliases,
          description = EXCLUDED.description,
          active = TRUE,
          updated_at = NOW()
      """, (
        food_type["slug"],
        food_type["name_en"],
        food_type["name_ja"],
        food_type["name_zh"],
        psycopg2.extras.Json(food_type.get("aliases") or []),
        food_type.get("description") or "",
      ))
    for food_type in FOOD_TAXONOMY:
      parent_slug = food_type.get("parent_slug") or ""
      if parent_slug:
        cur.execute("""
          UPDATE food_types AS child
          SET parent_id = parent.id
          FROM food_types AS parent
          WHERE child.slug = %s AND parent.slug = %s
        """, (food_type["slug"], parent_slug))
      else:
        cur.execute(
          "UPDATE food_types SET parent_id = NULL WHERE slug = %s",
          (food_type["slug"],),
        )
    cur.execute("""
      CREATE TABLE IF NOT EXISTS food_category_review_tasks (
        id            BIGSERIAL PRIMARY KEY,
        restaurant_id BIGINT NOT NULL REFERENCES restaurants(id) ON DELETE CASCADE,
        status        TEXT NOT NULL DEFAULT 'pending'
                      CHECK (status IN ('pending', 'completed')),
        reviewer_note TEXT,
        created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
        reviewed_at   TIMESTAMPTZ
      )
    """)
    cur.execute("""
      CREATE UNIQUE INDEX IF NOT EXISTS food_category_review_tasks_pending_restaurant_idx
      ON food_category_review_tasks(restaurant_id) WHERE status = 'pending'
    """)
    cur.execute("""
      CREATE INDEX IF NOT EXISTS food_category_review_tasks_status_id_idx
      ON food_category_review_tasks(status, id)
    """)
    cur.execute("""
      CREATE TABLE IF NOT EXISTS food_category_review_photos (
        id              BIGSERIAL PRIMARY KEY,
        review_task_id  BIGINT NOT NULL REFERENCES food_category_review_tasks(id) ON DELETE CASCADE,
        image_url       TEXT NOT NULL,
        source_page_url TEXT NOT NULL DEFAULT '',
        position        INTEGER NOT NULL DEFAULT 0,
        UNIQUE (review_task_id, image_url)
      )
    """)
    cur.execute("""
      CREATE TABLE IF NOT EXISTS food_category_review_candidates (
        id                     BIGSERIAL PRIMARY KEY,
        review_task_id         BIGINT NOT NULL REFERENCES food_category_review_tasks(id) ON DELETE CASCADE,
        suggested_food_type_id BIGINT REFERENCES food_types(id) ON DELETE RESTRICT,
        reviewed_food_type_id  BIGINT REFERENCES food_types(id) ON DELETE RESTRICT,
        decision               TEXT NOT NULL DEFAULT 'pending'
                               CHECK (decision IN ('pending', 'approved', 'rejected', 'changed', 'added')),
        origin                 TEXT NOT NULL DEFAULT 'model'
                               CHECK (origin IN ('model', 'reviewer')),
        created_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
        reviewed_at            TIMESTAMPTZ
      )
    """)
    cur.execute("""
      CREATE UNIQUE INDEX IF NOT EXISTS food_category_review_candidates_suggested_idx
      ON food_category_review_candidates(review_task_id, suggested_food_type_id)
      WHERE suggested_food_type_id IS NOT NULL
    """)
    cur.execute("""
      CREATE TABLE IF NOT EXISTS food_category_review_evidence (
        id              BIGSERIAL PRIMARY KEY,
        candidate_id    BIGINT NOT NULL REFERENCES food_category_review_candidates(id) ON DELETE CASCADE,
        image_url       TEXT NOT NULL DEFAULT '',
        source_page_url TEXT NOT NULL DEFAULT '',
        evidence_text   TEXT NOT NULL,
        block_ids       JSONB NOT NULL DEFAULT '[]'::JSONB,
        created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
        UNIQUE (candidate_id, image_url, evidence_text)
      )
    """)
    cur.execute("""
      CREATE INDEX IF NOT EXISTS food_category_review_evidence_candidate_idx
      ON food_category_review_evidence(candidate_id, id)
    """)
    cur.execute("""
      CREATE TABLE IF NOT EXISTS restaurant_food_types (
        restaurant_id      BIGINT NOT NULL REFERENCES restaurants(id) ON DELETE CASCADE,
        food_type_id       BIGINT NOT NULL REFERENCES food_types(id) ON DELETE RESTRICT,
        review_candidate_id BIGINT REFERENCES food_category_review_candidates(id) ON DELETE SET NULL,
        provenance         TEXT NOT NULL CHECK (provenance IN ('model_reviewed', 'reviewer_added')),
        created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
        PRIMARY KEY (restaurant_id, food_type_id)
      )
    """)
    cur.execute("""
      CREATE INDEX IF NOT EXISTS restaurant_food_types_food_type_idx
      ON restaurant_food_types(food_type_id, restaurant_id)
    """)
    cur.execute("""
      CREATE TABLE IF NOT EXISTS food_taxonomy_proposals (
        id                    BIGSERIAL PRIMARY KEY,
        review_task_id        BIGINT NOT NULL REFERENCES food_category_review_tasks(id) ON DELETE CASCADE,
        restaurant_id         BIGINT NOT NULL REFERENCES restaurants(id) ON DELETE CASCADE,
        proposed_name         TEXT NOT NULL,
        parent_food_type_id   BIGINT REFERENCES food_types(id) ON DELETE SET NULL,
        explanation           TEXT NOT NULL DEFAULT '',
        evidence              JSONB NOT NULL DEFAULT '[]'::JSONB,
        status                TEXT NOT NULL DEFAULT 'pending'
                              CHECK (status IN ('pending', 'created', 'mapped', 'rejected')),
        resolved_food_type_id BIGINT REFERENCES food_types(id) ON DELETE SET NULL,
        created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
        resolved_at           TIMESTAMPTZ
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


def get_legacy_review_restaurants(conn):
  """Return restaurants with reusable dish evidence but no category review yet."""
  with conn.cursor() as cur:
    cur.execute("""
      SELECT DISTINCT r.id, r.url
      FROM restaurants r
      JOIN menu_review_tasks task ON task.restaurant_id = r.id
      JOIN menu_review_items item ON item.review_task_id = task.id
      WHERE item.decision <> 'rejected'
        AND NOT EXISTS (
          SELECT 1
          FROM food_category_review_tasks category_task
          WHERE category_task.restaurant_id = r.id
        )
      ORDER BY r.id
    """)
    return cur.fetchall()


def record_log(conn, restaurant_id, status, error_msg=None):
  with conn.cursor() as cur:
    cur.execute("""
      INSERT INTO menu_scrape_log (restaurant_id, status, error_msg, attempted_at, completed_at)
      VALUES (%s, %s, %s, NOW(),
              CASE WHEN %s IN ('done', 'no_menu', 'review_pending', 'category_review_pending') THEN NOW() ELSE NULL END)
      ON CONFLICT (restaurant_id) DO UPDATE SET
        status       = EXCLUDED.status,
        error_msg    = EXCLUDED.error_msg,
        retry_count  = menu_scrape_log.retry_count + 1,
        attempted_at = NOW(),
        completed_at = EXCLUDED.completed_at
    """, (restaurant_id, status, error_msg, status))
  conn.commit()


def save_menu_items(conn, restaurant_id, items):
  with conn.cursor() as cur:
    cur.execute("DELETE FROM menu_items WHERE restaurant_id = %s", (restaurant_id,))
    if items:
      psycopg2.extras.execute_values(
        cur,
        """INSERT INTO menu_items (restaurant_id, name)
           VALUES %s""",
        [(restaurant_id, normalize(item["name"])) for item in items]
      )
  conn.commit()


def save_menu_review_task(
  conn,
  restaurant_id,
  image_url,
  source_page_url,
  items,
  refresh_completed=False,
):
  """Queue one image, matching old thumbnail URLs to their original-resolution source."""
  review_items = merge_review_items(items)
  if not review_items and not refresh_completed:
    return False

  with conn.cursor() as cur:
    cur.execute("""
      SELECT id, status
      FROM menu_review_tasks
      WHERE restaurant_id = %s
        AND regexp_replace(image_url, '/[0-9]+x[0-9]+_rect_', '/') =
            regexp_replace(%s, '/[0-9]+x[0-9]+_rect_', '/')
      ORDER BY CASE WHEN image_url = %s THEN 0 ELSE 1 END, id
      LIMIT 1
      FOR UPDATE
    """, (restaurant_id, image_url, image_url))
    row = cur.fetchone()

    # A refreshed photo can legitimately produce no candidates when it is a
    # drink-only menu. Retire only its old pending task and retain the rejected
    # items as audit history; completed human reviews are never changed here.
    if not review_items:
      if row and row[1] == "pending":
        task_id = row[0]
        cur.execute("""
          DELETE FROM menu_items
          WHERE review_item_id IN (
            SELECT id FROM menu_review_items WHERE review_task_id = %s
          )
        """, (task_id,))
        cur.execute("""
          UPDATE menu_review_items
          SET decision = 'rejected', reviewed_name = NULL, reviewed_at = NOW()
          WHERE review_task_id = %s
        """, (task_id,))
        cur.execute("""
          UPDATE menu_review_tasks
          SET status = 'completed',
              reviewer_note = 'Automatically excluded during refresh because no food items were found.',
              reviewed_at = NOW()
          WHERE id = %s
        """, (task_id,))
      conn.commit()
      return False

    if row:
      task_id, status = row
      if status == "completed" and not refresh_completed:
        conn.commit()
        return False
      if status == "completed":
        cur.execute("""
          DELETE FROM menu_items
          WHERE review_item_id IN (
            SELECT id FROM menu_review_items WHERE review_task_id = %s
          )
        """, (task_id,))
      cur.execute("""
        UPDATE menu_review_tasks
        SET image_url = %s,
            source_page_url = %s,
            status = 'pending',
            reviewer_note = NULL,
            reviewed_at = NULL,
            created_at = NOW()
        WHERE id = %s
      """, (image_url, source_page_url, task_id))
    else:
      cur.execute("""
        INSERT INTO menu_review_tasks (restaurant_id, image_url, source_page_url)
        VALUES (%s, %s, %s)
        RETURNING id
      """, (restaurant_id, image_url, source_page_url))
      task_id = cur.fetchone()[0]

    cur.execute("DELETE FROM menu_review_items WHERE review_task_id = %s", (task_id,))
    psycopg2.extras.execute_values(
      cur,
      """INSERT INTO menu_review_items
         (review_task_id, extracted_name, evidence_block_ids)
         VALUES %s""",
      [
        (
          task_id,
          item["name"],
          psycopg2.extras.Json(item.get("evidence_block_ids") or []),
        )
        for item in review_items
      ],
    )
  conn.commit()
  return True


def load_legacy_menu_evidence(conn, restaurant_id):
  """Reuse previously extracted dish names so migration does not repay OCR cost."""
  with conn.cursor() as cur:
    cur.execute("""
      SELECT i.id, t.image_url, t.source_page_url,
             COALESCE(NULLIF(i.reviewed_name, ''), i.extracted_name),
             i.evidence_block_ids
      FROM menu_review_items i
      JOIN menu_review_tasks t ON t.id = i.review_task_id
      WHERE t.restaurant_id = %s
        AND i.decision <> 'rejected'
      ORDER BY t.id, i.id
    """, (restaurant_id,))
    return [
      {
        "id": row[0],
        "image_url": row[1],
        "source_page_url": row[2],
        "text": row[3],
        "block_ids": row[4] or [],
      }
      for row in cur.fetchall()
      if normalize(row[3])
    ]


def save_food_category_review_task(
  conn,
  restaurant_id,
  categories,
  photos=None,
  refresh_pending=False,
):
  """Create or refresh one restaurant-level category review task."""
  categories = merge_category_candidates(categories)
  photos = photos or []
  with conn.cursor() as cur:
    cur.execute("""
      SELECT id
      FROM food_category_review_tasks
      WHERE restaurant_id = %s AND status = 'pending'
      LIMIT 1
      FOR UPDATE
    """, (restaurant_id,))
    row = cur.fetchone()
    task_id = row[0] if row else None

    cur.execute("""
      SELECT DISTINCT f.slug
      FROM food_category_review_candidates c
      JOIN food_category_review_tasks t ON t.id = c.review_task_id
      JOIN food_types f ON f.id = c.suggested_food_type_id
      WHERE t.restaurant_id = %s AND t.status = 'completed'
      UNION
      SELECT f.slug
      FROM restaurant_food_types link
      JOIN food_types f ON f.id = link.food_type_id
      WHERE link.restaurant_id = %s
    """, (restaurant_id, restaurant_id))
    decided_slugs = {item[0] for item in cur.fetchall()}
    categories = [
      category for category in categories
      if category["food_type"] not in decided_slugs
    ]

    if task_id and refresh_pending:
      cur.execute(
        "DELETE FROM food_category_review_candidates WHERE review_task_id = %s",
        (task_id,),
      )
      cur.execute(
        "DELETE FROM food_category_review_photos WHERE review_task_id = %s",
        (task_id,),
      )
    elif task_id:
      cur.execute("""
        SELECT f.slug
        FROM food_category_review_candidates c
        JOIN food_types f ON f.id = c.suggested_food_type_id
        WHERE c.review_task_id = %s
      """, (task_id,))
      pending_slugs = {item[0] for item in cur.fetchall()}
      categories = [
        category for category in categories
        if category["food_type"] not in pending_slugs
      ]

    if not categories and not photos:
      if task_id and refresh_pending:
        cur.execute("""
          UPDATE food_category_review_tasks
          SET status = 'completed',
              reviewer_note = 'Automatically closed during refresh because no new food categories were found.',
              reviewed_at = NOW()
          WHERE id = %s
        """, (task_id,))
      conn.commit()
      return None

    if task_id is None:
      cur.execute("""
        INSERT INTO food_category_review_tasks (restaurant_id)
        VALUES (%s)
        RETURNING id
      """, (restaurant_id,))
      task_id = cur.fetchone()[0]
    elif refresh_pending:
      cur.execute("""
        UPDATE food_category_review_tasks
        SET created_at = NOW(), reviewer_note = NULL, reviewed_at = NULL
        WHERE id = %s
        """, (task_id,))

    for position, photo in enumerate(photos):
      image_url = original_tabelog_image_url(photo.get("image_url") or "")
      if not image_url:
        continue
      cur.execute("""
        INSERT INTO food_category_review_photos
          (review_task_id, image_url, source_page_url, position)
        VALUES (%s, %s, %s, %s)
        ON CONFLICT (review_task_id, image_url) DO UPDATE SET
          source_page_url = EXCLUDED.source_page_url,
          position = EXCLUDED.position
      """, (
        task_id,
        image_url,
        photo.get("source_page_url") or "",
        position,
      ))

    inserted = 0
    for category in categories:
      cur.execute("""
        INSERT INTO food_category_review_candidates
          (review_task_id, suggested_food_type_id)
        SELECT %s, id FROM food_types WHERE slug = %s AND active = TRUE
        ON CONFLICT (review_task_id, suggested_food_type_id)
          WHERE suggested_food_type_id IS NOT NULL
        DO NOTHING
        RETURNING id
      """, (task_id, category["food_type"]))
      candidate_row = cur.fetchone()
      if candidate_row is None:
        continue
      candidate_id = candidate_row[0]
      inserted += 1
      for evidence in category.get("evidence") or []:
        evidence_text = normalize(evidence.get("text") or "")
        if not evidence_text:
          continue
        cur.execute("""
          INSERT INTO food_category_review_evidence
            (candidate_id, image_url, source_page_url, evidence_text, block_ids)
          VALUES (%s, %s, %s, %s, %s)
          ON CONFLICT (candidate_id, image_url, evidence_text) DO UPDATE SET
            source_page_url = EXCLUDED.source_page_url,
            block_ids = EXCLUDED.block_ids
        """, (
          candidate_id,
          evidence.get("image_url") or "",
          evidence.get("source_page_url") or "",
          evidence_text,
          psycopg2.extras.Json(evidence.get("block_ids") or []),
        ))
  conn.commit()
  return {"task_id": task_id, "category_count": inserted}


# ── Scraping helpers ───────────────────────────────────────────────────────────

def normalize(name):
  return unicodedata.normalize("NFKC", str(name)).strip()


def evidence_key(value):
  """Normalize harmless OCR/Markdown formatting without fuzzy-matching names."""
  return re.sub(r"[\s*_`|]+", "", normalize(value))


def clean_candidate_name(value):
  name = normalize(value)
  name = re.sub(r"^[\s|·•・●▪◦\-–—]+", "", name)
  name = re.sub(
    r"\s*[\(（]\s*\d+(?:\.\d+)?\s*(?:ml|mL|ML|l|L|ℓ)\s*[\)）]\s*$",
    "",
    name,
  )
  return name.strip()


def original_tabelog_image_url(url):
  """Replace Tabelog's low-resolution square derivative with the original image URL."""
  parts = urlsplit(url)
  path = re.sub(r"/\d+x\d+_rect_([^/]+)$", r"/\1", parts.path)
  return urlunsplit((parts.scheme, parts.netloc, path, parts.query, parts.fragment))


def get_nvidia_client():
  global _nvidia_client
  if _nvidia_client is None:
    api_key = os.environ.get("NVIDIA_Inference_Key") or os.environ.get("NVIDIA_API_KEY")
    if not api_key:
      raise RuntimeError("Missing NVIDIA_Inference_Key in .env")
    _nvidia_client = OpenAI(base_url=NVIDIA_BASE_URL, api_key=api_key)
  return _nvidia_client


def call_vlm(messages, max_retries=6):
  last_exc = RuntimeError("no attempts made")
  with _vlm_semaphore:
    for attempt in range(max_retries):
      try:
        return get_nvidia_client().chat.completions.create(
          model=VLM_MODEL,
          messages=messages,
          temperature=0.1,
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


def validate_vlm_access():
  """Fail once, before paid OCR fan-out, if the configured NVIDIA route is unusable."""
  global _vlm_access_validated
  if _vlm_access_validated:
    return
  with _vlm_access_lock:
    if _vlm_access_validated:
      return
    call_vlm(
      [{"role": "user", "content": "Return only an empty JSON array: []"}],
      max_retries=1,
    )
    _vlm_access_validated = True


def call_ocr(image_data, mime_type, max_retries=6):
  api_key = os.environ.get("MISTRAL_KEY") or os.environ.get("MISTRAL_API_KEY")
  if not api_key:
    raise RuntimeError("Missing MISTRAL_KEY in .env")

  encoded = base64.standard_b64encode(image_data).decode("utf-8")
  payload = {
    "model": MISTRAL_OCR_MODEL,
    "document": {
      "type": "image_url",
      "image_url": f"data:{mime_type};base64,{encoded}",
    },
    "include_blocks": True,
    "confidence_scores_granularity": "word",
  }
  headers = {
    "Authorization": f"Bearer {api_key}",
    "Content-Type": "application/json",
  }

  last_exc = RuntimeError("no attempts made")
  with _ocr_semaphore:
    for attempt in range(max_retries):
      try:
        response = httpx.post(MISTRAL_OCR_URL, headers=headers, json=payload, timeout=90)
        if response.status_code in (408, 425, 429) or response.status_code >= 500:
          if attempt < max_retries - 1:
            retry_after = response.headers.get("retry-after")
            try:
              wait = float(retry_after) if retry_after else 2 ** attempt
            except ValueError:
              wait = 2 ** attempt
            wait += random.uniform(0, 1)
            tprint(f"  OCR temporarily unavailable ({response.status_code}), retrying in {wait:.1f}s...")
            time.sleep(wait)
            continue
        response.raise_for_status()
        return response.json()
      except (httpx.ConnectError, httpx.TimeoutException, httpx.RemoteProtocolError) as e:
        last_exc = e
        if attempt < max_retries - 1:
          wait = (2 ** attempt) + random.uniform(0, 1)
          tprint(f"  OCR network error (attempt {attempt+1}), retrying in {wait:.1f}s...")
          time.sleep(wait)
        else:
          raise
  raise last_exc


def fetch_html(url, retries=3):
  last_exc = RuntimeError("no attempts made")
  for attempt in range(retries):
    time.sleep(DELAY)
    try:
      response = requests.get(url, headers=HEADERS, timeout=15)
    except requests.RequestException as e:
      last_exc = e
      if attempt < retries - 1:
        time.sleep((2 ** attempt) + random.uniform(0, 1))
        continue
      raise

    if response.status_code == 200:
      return response
    if response.status_code == 404:
      return None

    retryable = response.status_code in (408, 425, 429) or response.status_code >= 500
    if retryable and attempt < retries - 1:
      retry_after = response.headers.get("retry-after")
      try:
        wait = float(retry_after) if retry_after else 2 ** attempt
      except ValueError:
        wait = 2 ** attempt
      time.sleep(wait + random.uniform(0, 1))
      continue

    raise RuntimeError(f"Tabelog request failed with HTTP {response.status_code}: {url}")
  raise last_exc


def extract_html_menu_items(url):
  r = fetch_html(url)
  if not r:
    return []
  soup = BeautifulSoup(r.text, "html.parser")
  items = []
  for item in soup.select(".js-menu-photo-list-target, .c-menu-parts__item, .menu-item"):
    name_el = item.select_one(".c-mv-tl, .menu-item__name")
    if name_el:
      items.append({
        "name": name_el.get_text(strip=True),
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
      src = original_tabelog_image_url(str(a.get("href") or ""))
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


def detect_image_mime(image_data, declared_type=None):
  declared_type = (declared_type or "").split(";", 1)[0].strip().lower()
  if declared_type.startswith("image/"):
    return declared_type
  if image_data.startswith(b"\xff\xd8\xff"):
    return "image/jpeg"
  if image_data.startswith(b"\x89PNG\r\n\x1a\n"):
    return "image/png"
  if image_data.startswith((b"GIF87a", b"GIF89a")):
    return "image/gif"
  if image_data[:4] in (b"RIFF",) and image_data[8:12] == b"WEBP":
    return "image/webp"
  return "image/jpeg"


def build_ocr_tiles(image_data, mime_type, grid=None):
  """Return overlapping in-memory crops so small menu text reaches OCR at useful scale."""
  grid = OCR_TILE_GRID if grid is None else max(1, grid)
  if grid == 1:
    return [{"id": "full", "data": image_data, "mime_type": mime_type}]

  try:
    image = ImageOps.exif_transpose(Image.open(BytesIO(image_data))).convert("RGB")
  except (UnidentifiedImageError, OSError):
    return [{"id": "full", "data": image_data, "mime_type": mime_type}]

  width, height = image.size
  if width < 400 or height < 400:
    return [{"id": "full", "data": image_data, "mime_type": mime_type}]

  overlap_x = round(width * OCR_TILE_OVERLAP)
  overlap_y = round(height * OCR_TILE_OVERLAP)
  tiles = []
  for row in range(grid):
    for column in range(grid):
      left = max(0, round(column * width / grid) - overlap_x)
      top = max(0, round(row * height / grid) - overlap_y)
      right = min(width, round((column + 1) * width / grid) + overlap_x)
      bottom = min(height, round((row + 1) * height / grid) + overlap_y)
      output = BytesIO()
      image.crop((left, top, right, bottom)).save(
        output, format="JPEG", quality=95, optimize=True
      )
      tiles.append({
        "id": f"t{row}_{column}",
        "data": output.getvalue(),
        "mime_type": "image/jpeg",
      })
  return tiles


def fetch_image_with_retry(url, retries=3):
  last_exc = RuntimeError("no attempts made")
  for attempt in range(retries):
    try:
      response = httpx.get(url, timeout=15, follow_redirects=True)
      response.raise_for_status()
      return response.content, detect_image_mime(response.content, response.headers.get("content-type"))
    except (httpx.ConnectError, httpx.TimeoutException, httpx.RemoteProtocolError) as e:
      last_exc = e
      if attempt < retries - 1:
        time.sleep(2 ** attempt)
  raise last_exc


def extract_ocr_blocks(ocr_response, id_prefix=""):
  """Return prompt-ready OCR blocks plus all OCR text used for evidence checks."""
  blocks = []
  all_text = []
  for page_index, page in enumerate(ocr_response.get("pages") or []):
    markdown = page.get("markdown") or ""
    if markdown:
      all_text.append(markdown)

    page_blocks = page.get("blocks") or []
    for block_index, block in enumerate(page_blocks):
      content = block.get("content") or ""
      if not isinstance(content, str):
        content = json.dumps(content, ensure_ascii=False)
      if not content.strip():
        continue
      blocks.append({
        "id": f"{id_prefix}p{page_index}_b{block_index}",
        "type": block.get("type") or "text",
        "content": content,
      })

    if not page_blocks and markdown.strip():
      blocks.append({
        "id": f"{id_prefix}p{page_index}",
        "type": "text",
        "content": markdown,
      })

  return blocks, "\n".join(all_text)


def format_ocr_blocks(blocks):
  return "\n".join(
    f"[{block['id']} type={block['type']}] {block['content']}"
    for block in blocks
  )


def is_drink_only_menu(blocks):
  """Detect a drink-page title near the start of the first OCR tile."""
  drink_titles = {
    "DRINKMENU",
    "BEVERAGEMENU",
    "ドリンクメニュー",
    "お飲み物",
    "おのみもの",
    "飲み物メニュー",
    "飲料メニュー",
  }
  checked = 0
  for block in blocks:
    content = normalize(block.get("content") or "")
    if not content or content.startswith("!["):
      continue
    checked += 1
    title = re.sub(r"[#\s*_`|:：·•・\-–—]+", "", content).upper()
    if title in drink_titles:
      return True
    if checked >= 2:
      break
  return False


def validate_item_candidates(items, blocks):
  """Keep only names explicitly present in the OCR blocks cited by Gemini."""
  block_lookup = {block["id"]: block["content"] for block in blocks}
  excluded_blocks = set()
  excluding_serving_options = False
  for block in blocks:
    content = normalize(block["content"])
    if "飲み方をお選び" in content:
      excluding_serving_options = True
      excluded_blocks.add(block["id"])
      continue
    if block.get("type") == "title" or content.lstrip().startswith("#"):
      excluding_serving_options = False
    elif excluding_serving_options:
      excluded_blocks.add(block["id"])

  validated = []
  for item in items:
    if not isinstance(item, dict):
      continue
    name = clean_candidate_name(item.get("name", ""))
    block_ids = item.get("evidence_block_ids") or []
    if isinstance(block_ids, str):
      block_ids = [block_ids]
    valid_block_ids = [
      block_id for block_id in block_ids
      if block_id in block_lookup and block_id not in excluded_blocks
    ]
    evidence = "\n".join(block_lookup[block_id] for block_id in valid_block_ids)
    if name and evidence and evidence_key(name) in evidence_key(evidence):
      validated.append({"name": name, "evidence_block_ids": valid_block_ids})
  return validated


def estimate_priced_menu_rows(blocks):
  """Estimate expected candidates from OCR rows that contain a parenthesized tax price."""
  count = 0
  for block in blocks:
    for line in str(block.get("content") or "").splitlines():
      price = re.search(r"\d[\d,]*\s*[\(（]\s*\d[\d,]*\s*[\)）]", line)
      name_prefix = line[:price.start()] if price else ""
      if price and len(evidence_key(name_prefix)) >= 2:
        count += 1
  return count


def classify_ocr_blocks(image_bytes, mime_type, ocr_blocks, label="", max_continuations=3):
  image_data = base64.standard_b64encode(image_bytes).decode("utf-8")
  image_content = {
    "type": "image_url",
    "image_url": {"url": f"data:{mime_type};base64,{image_data}"},
  }
  prompt = ITEM_NAME_PROMPT.format(ocr_blocks=format_ocr_blocks(ocr_blocks))
  expected_rows = estimate_priced_menu_rows(ocr_blocks)
  minimum_recall = max(1, expected_rows // 2)
  best_items = []

  for attempt in range(2):
    all_items = []
    messages = [{
      "role": "user",
      "content": [image_content, {"type": "text", "text": prompt}],
    }]
    for _ in range(1 + max_continuations):
      completion = call_vlm(messages)
      raw = (completion.choices[0].message.content or "").strip()
      items = validate_item_candidates(
        parse_json_response(raw, label=label), ocr_blocks
      )
      all_items.extend(items)
      if not was_truncated(raw) or not items:
        break
      last_name = items[-1].get("name", "")
      tprint(
        f"  {label} Truncated after {len(items)} items, "
        f"continuing from '{last_name}'..."
      )
      messages.append({"role": "assistant", "content": raw})
      messages.append({"role": "user", "content": [
        image_content,
        {"type": "text", "text": (
          f"The previous response was cut off. The last item extracted was \"{last_name}\". "
          "Continue with remaining item names after that item. Every name must still cite one of "
          "the OCR block IDs supplied in the original request. Return only the JSON array."
        )}
      ]})
      time.sleep(0.5)

    attempt_items = merge_review_items(all_items)
    if len(attempt_items) > len(best_items):
      best_items = attempt_items
    if expected_rows < 4 or len(best_items) >= minimum_recall or attempt == 1:
      break
    tprint(
      f"  {label} Low recall ({len(best_items)}/{expected_rows} priced rows); "
      "retrying classification once..."
    )

  return best_items


def extract_items_from_photo(image_url, index=None, total=None, max_continuations=3):
  label = f"[{index}/{total}]" if index is not None else ""
  try:
    image_url = original_tabelog_image_url(image_url)
    image_bytes, mime_type = fetch_image_with_retry(image_url)
    tiles = build_ocr_tiles(image_bytes, mime_type)
    all_items = []
    successful_tiles = 0
    for tile_index, tile in enumerate(tiles):
      tile_label = f"{label} {tile['id']}".strip()
      try:
        ocr_response = call_ocr(tile["data"], tile["mime_type"])
        id_prefix = "" if len(tiles) == 1 else f"{tile['id']}_"
        ocr_blocks, _ = extract_ocr_blocks(ocr_response, id_prefix=id_prefix)
        if not ocr_blocks:
          tprint(f"  {tile_label} → OCR found no readable text")
          successful_tiles += 1
          continue
        if tile_index == 0 and is_drink_only_menu(ocr_blocks):
          tprint(f"  {label} → drink-only menu ignored after first OCR tile")
          return []
        validate_vlm_access()
        all_items.extend(classify_ocr_blocks(
          tile["data"],
          tile["mime_type"],
          ocr_blocks,
          label=tile_label,
          max_continuations=max_continuations,
        ))
        successful_tiles += 1
      except Exception as e:
        tprint(
          f"  {tile_label} tile failed "
          f"({type(e).__name__}: {str(e)[:80]})"
        )

    if successful_tiles == 0:
      raise RuntimeError("OCR/VLM processing failed for every image tile")
    items = merge_review_items(all_items)
    tprint(f"  {label} → {len(items)} items extracted from {len(tiles)} tile(s)")
    return items
  except Exception as e:
    tprint(f"  {label} Skipping photo (error: {type(e).__name__}: {str(e)[:80]})")
    return None


def merge_items(all_items):
  if not all_items:
    return []
  seen = {}
  for item in all_items:
    name = normalize(item.get("name", ""))
    if not name:
      continue
    seen.setdefault(name, {"name": name})
  return list(seen.values())


def merge_review_items(all_items):
  """Exact-dedupe review candidates while retaining their OCR evidence references."""
  seen = {}
  for item in all_items or []:
    name = normalize(item.get("name", ""))
    if not name:
      continue
    entry = seen.setdefault(name, {"name": name, "evidence_block_ids": []})
    for block_id in item.get("evidence_block_ids") or []:
      if block_id not in entry["evidence_block_ids"]:
        entry["evidence_block_ids"].append(block_id)
  return list(seen.values())


def validate_category_candidates(candidates, blocks):
  """Keep only taxonomy categories with exact, cited menu evidence."""
  block_lookup = {block["id"]: block for block in blocks}
  validated = []
  for candidate in candidates or []:
    if not isinstance(candidate, dict):
      continue
    food_type = normalize(candidate.get("food_type") or "").lower()
    if food_type not in FOOD_TYPE_BY_SLUG:
      continue
    valid_evidence = []
    for evidence in candidate.get("evidence") or []:
      if not isinstance(evidence, dict):
        continue
      text = normalize(evidence.get("text") or "")
      block_id = str(evidence.get("block_id") or "")
      block = block_lookup.get(block_id)
      if not text or block is None:
        continue
      if evidence_key(text) not in evidence_key(block.get("content") or ""):
        continue
      valid_evidence.append({"text": text, "block_ids": [block_id]})
    if valid_evidence:
      validated.append({"food_type": food_type, "evidence": valid_evidence})
  return merge_category_candidates(validated)


def merge_category_candidates(candidates):
  """Deduplicate categories, retain evidence, and remove redundant ancestors."""
  merged = {}
  for candidate in candidates or []:
    food_type = normalize(candidate.get("food_type") or "").lower()
    if food_type not in FOOD_TYPE_BY_SLUG:
      continue
    entry = merged.setdefault(food_type, {"food_type": food_type, "evidence": []})
    evidence_by_key = {
      (
        evidence.get("image_url") or "",
        normalize(evidence.get("text") or ""),
      ): evidence
      for evidence in entry["evidence"]
    }
    for evidence in candidate.get("evidence") or []:
      text = normalize(evidence.get("text") or "")
      if not text:
        continue
      key = (evidence.get("image_url") or "", text)
      existing = evidence_by_key.get(key)
      if existing is None:
        existing = {
          "text": text,
          "image_url": evidence.get("image_url") or "",
          "source_page_url": evidence.get("source_page_url") or "",
          "block_ids": list(dict.fromkeys(evidence.get("block_ids") or [])),
        }
        entry["evidence"].append(existing)
        evidence_by_key[key] = existing
      else:
        for block_id in evidence.get("block_ids") or []:
          if block_id not in existing["block_ids"]:
            existing["block_ids"].append(block_id)
  selected_slugs = set(merged)
  redundant_ancestors = {
    ancestor
    for slug in selected_slugs
    for ancestor in food_type_ancestors(slug)
    if ancestor in selected_slugs
  }
  return [
    category
    for slug, category in merged.items()
    if slug not in redundant_ancestors
  ]


def classify_category_blocks(blocks, image_bytes=None, mime_type=None, label=""):
  if not blocks:
    return []
  prompt = CATEGORY_PROMPT.format(
    taxonomy=FOOD_TAXONOMY_PROMPT,
    ocr_blocks=format_ocr_blocks(blocks),
  )
  content = []
  if image_bytes is not None and mime_type:
    image_data = base64.standard_b64encode(image_bytes).decode("utf-8")
    content.append({
      "type": "image_url",
      "image_url": {"url": f"data:{mime_type};base64,{image_data}"},
    })
  content.append({"type": "text", "text": prompt})
  completion = call_vlm([{"role": "user", "content": content}])
  raw = (completion.choices[0].message.content or "").strip()
  return validate_category_candidates(parse_json_response(raw, label=label), blocks)


def extract_categories_from_photo(image_url, index=None, total=None):
  """Run tiled OCR and controlled-taxonomy classification for one menu photo."""
  label = f"[{index}/{total}]" if index is not None else ""
  image_url = original_tabelog_image_url(image_url)
  try:
    image_bytes, mime_type = fetch_image_with_retry(image_url)
    tiles = build_ocr_tiles(image_bytes, mime_type)
    all_categories = []
    successful_tiles = 0
    for tile_index, tile in enumerate(tiles):
      tile_label = f"{label} {tile['id']}".strip()
      try:
        ocr_response = call_ocr(tile["data"], tile["mime_type"])
        id_prefix = "" if len(tiles) == 1 else f"{tile['id']}_"
        blocks, _ = extract_ocr_blocks(ocr_response, id_prefix=id_prefix)
        if not blocks:
          tprint(f"  {tile_label} → OCR found no readable text")
          successful_tiles += 1
          continue
        if tile_index == 0 and is_drink_only_menu(blocks):
          tprint(f"  {label} → drink-only menu ignored after first OCR tile")
          return {"categories": [], "image_url": image_url, "drink_only": True}
        categories = classify_category_blocks(
          blocks,
          image_bytes=tile["data"],
          mime_type=tile["mime_type"],
          label=tile_label,
        )
        for category in categories:
          for evidence in category["evidence"]:
            evidence["image_url"] = image_url
        all_categories.extend(categories)
        successful_tiles += 1
      except Exception as error:
        tprint(
          f"  {tile_label} tile failed "
          f"({type(error).__name__}: {str(error)[:80]})"
        )
    if successful_tiles == 0:
      raise RuntimeError("OCR/VLM processing failed for every image tile")
    categories = merge_category_candidates(all_categories)
    tprint(f"  {label} → {len(categories)} food types from {len(tiles)} tile(s)")
    return {"categories": categories, "image_url": image_url, "drink_only": False}
  except Exception as error:
    tprint(f"  {label} Skipping photo (error: {type(error).__name__}: {str(error)[:80]})")
    return None


def classify_structured_menu_items(items, source_page_url):
  categories = []
  for offset in range(0, len(items), 80):
    blocks = [
      {
        "id": f"html_{offset + index}",
        "type": "menu_item",
        "content": normalize(item.get("name") or ""),
      }
      for index, item in enumerate(items[offset:offset + 80])
      if normalize(item.get("name") or "")
    ]
    for category in classify_category_blocks(blocks, label="structured menu"):
      for evidence in category["evidence"]:
        evidence["source_page_url"] = source_page_url
      categories.append(category)
  return merge_category_candidates(categories)


def classify_legacy_menu_evidence(evidence_rows):
  """Classify old dish candidates by photo while preserving their source evidence."""
  groups = {}
  for row in evidence_rows:
    key = (row["image_url"], row["source_page_url"])
    groups.setdefault(key, []).append(row)

  def classify_group(group_key, rows):
    image_url, source_page_url = group_key
    group_categories = []
    for offset in range(0, len(rows), 80):
      chunk = rows[offset:offset + 80]
      blocks = [
        {
          "id": f"legacy_{row['id']}",
          "type": "menu_item",
          "content": row["text"],
        }
        for row in chunk
      ]
      row_by_block = {f"legacy_{row['id']}": row for row in chunk}
      categories = classify_category_blocks(blocks, label=f"legacy {image_url}")
      for category in categories:
        for item in category["evidence"]:
          source_block_id = item["block_ids"][0]
          source = row_by_block[source_block_id]
          item["image_url"] = image_url
          item["source_page_url"] = source_page_url
          item["block_ids"] = source["block_ids"] or [source_block_id]
        group_categories.append(category)
    return group_categories

  categories = []
  with ThreadPoolExecutor(max_workers=VLM_WORKERS) as executor:
    futures = [
      executor.submit(classify_group, key, rows)
      for key, rows in groups.items()
    ]
    for future in as_completed(futures):
      categories.extend(future.result())
  return merge_category_candidates(categories)


def scrape_food_category_result(restaurant_url):
  url = restaurant_url.rstrip("/") + "/"
  structured_url = url + "dtlmenu/"
  html_items = extract_html_menu_items(structured_url)
  if html_items:
    categories = classify_structured_menu_items(html_items, structured_url)
    if categories:
      return {"source": "html", "categories": categories, "photos": []}

  photo_url = url + "dtlmenu/photo/"
  photos = fetch_photo_urls(photo_url)
  if not photos:
    return {"source": "none", "categories": [], "photos": []}

  categories = []
  successful_photos = 0
  included_photos = set()
  with ThreadPoolExecutor(max_workers=VLM_WORKERS) as executor:
    futures = {
      executor.submit(extract_categories_from_photo, image_url, index, len(photos)): image_url
      for index, image_url in enumerate(photos, 1)
    }
    for future in as_completed(futures):
      photo_result = future.result()
      if photo_result is None:
        continue
      successful_photos += 1
      if photo_result["drink_only"]:
        continue
      included_photos.add(photo_result["image_url"])
      for category in photo_result["categories"]:
        for evidence in category["evidence"]:
          evidence["source_page_url"] = photo_url
      categories.extend(photo_result["categories"])
  if successful_photos == 0:
    raise RuntimeError("OCR/VLM processing failed for every menu photo")
  return {
    "source": "photo",
    "categories": merge_category_candidates(categories),
    "photos": [
      {"image_url": image_url, "source_page_url": photo_url}
      for image_url in photos
      if original_tabelog_image_url(image_url) in included_photos
    ],
  }


def scrape_menu_result(restaurant_url, on_photo_result=None):
  url = restaurant_url.rstrip("/") + "/"

  html_items = extract_html_menu_items(url + "dtlmenu/")
  if html_items:
    return {"source": "html", "items": merge_items(html_items)}

  photo_url = url + "dtlmenu/photo/"
  photos = fetch_photo_urls(photo_url)
  if not photos:
    return {"source": "none", "items": []}

  all_items = []
  successful_photos = 0
  total = len(photos)
  with ThreadPoolExecutor(max_workers=VLM_WORKERS) as executor:
    futures = {
      executor.submit(extract_items_from_photo, img_url, i, total): img_url
      for i, img_url in enumerate(photos, 1)
    }
    for future in as_completed(futures):
      items = future.result()
      if items is not None:
        successful_photos += 1
        review_items = merge_review_items(items)
        all_items.extend(review_items)
        if on_photo_result is not None:
          on_photo_result(futures[future], review_items, photo_url)

  if successful_photos == 0:
    raise RuntimeError("OCR/VLM processing failed for every menu photo")

  return {"source": "photo", "items": merge_items(all_items)}


def scrape_menu(restaurant_url, on_photo_result=None):
  """Compatibility wrapper returning the extracted item list or None for no menu."""
  result = scrape_menu_result(restaurant_url, on_photo_result=on_photo_result)
  if result["source"] == "none":
    return None
  return result["items"]


def scrape_food_categories(restaurant_url):
  """Return controlled food-category candidates for one restaurant."""
  result = scrape_food_category_result(restaurant_url)
  if result["source"] == "none":
    return None
  return result["categories"]


# ── Main ───────────────────────────────────────────────────────────────────────

def main():
  parser = argparse.ArgumentParser(
    description="Classify restaurant menus into reviewable food categories"
  )
  parser.add_argument("--retry-errors", action="store_true",
                      help="retry restaurants that previously failed (up to MAX_RETRIES times)")
  parser.add_argument("--offset", type=int, default=0,
                      help="skip the first N pending restaurants (for splitting work across machines)")
  parser.add_argument("--limit", type=int, default=0,
                      help="stop after processing this many restaurants (0 = no limit)")
  parser.add_argument("--restaurant-url",
                      help="process one exact restaurant URL regardless of scrape-log status")
  parser.add_argument("--refresh-reviews", action="store_true",
                      help="replace existing review tasks; requires --restaurant-url")
  parser.add_argument("--force-live-ocr", action="store_true",
                      help="ignore reusable legacy dish evidence and run the live OCR pipeline")
  parser.add_argument("--migrate-legacy-reviews", action="store_true",
                      help="classify restaurants that have legacy dish-review evidence")
  args = parser.parse_args()

  if args.refresh_reviews and not args.restaurant_url:
    parser.error("--refresh-reviews requires --restaurant-url")

  conn = psycopg2.connect(DSN)
  ensure_tables(conn)

  if args.restaurant_url:
    normalized_url = args.restaurant_url.rstrip("/") + "/"
    with conn.cursor() as cur:
      cur.execute("""
        SELECT id, url
        FROM restaurants
        WHERE rtrim(url, '/') = rtrim(%s, '/')
        LIMIT 1
      """, (normalized_url,))
      restaurant = cur.fetchone()
    if restaurant is None:
      conn.close()
      parser.error(f"restaurant URL is not present in the database: {normalized_url}")
    pending = [restaurant]
  elif args.migrate_legacy_reviews:
    pending = get_legacy_review_restaurants(conn)
  else:
    pending = get_pending_restaurants(conn, retry_errors=args.retry_errors)
  total = len(pending)
  print(f"Pending restaurants: {total}")

  if args.offset:
    pending = pending[args.offset:]
    print(f"Skipping first {args.offset} (starting at index {args.offset})")

  if args.limit:
    pending = pending[:args.limit]
    print(f"Limiting to {args.limit}")

  queued_for_review, skipped_no_menu, no_new_categories, failed = 0, 0, 0, 0

  for i, (rst_id, url) in enumerate(pending, 1):
    print(f"\n[{i}/{len(pending)}] {url}")
    t_start = time.time()

    try:
      legacy_evidence = [] if args.force_live_ocr else load_legacy_menu_evidence(conn, rst_id)
      if legacy_evidence:
        print(f"  Reusing {len(legacy_evidence)} previously extracted menu names")
        legacy_photos = []
        seen_photos = set()
        for evidence in legacy_evidence:
          image_url = original_tabelog_image_url(evidence["image_url"])
          if not image_url or image_url in seen_photos:
            continue
          seen_photos.add(image_url)
          legacy_photos.append({
            "image_url": image_url,
            "source_page_url": evidence["source_page_url"],
          })
        result = {
          "source": "legacy",
          "categories": classify_legacy_menu_evidence(legacy_evidence),
          "photos": legacy_photos,
        }
      else:
        result = scrape_food_category_result(url)

      review_task = save_food_category_review_task(
        conn,
        rst_id,
        result["categories"],
        photos=result.get("photos") or [],
        refresh_pending=args.refresh_reviews,
      )
    except Exception as e:
      conn.rollback()
      print(f"  ERROR: {type(e).__name__}: {str(e)[:120]}")
      record_log(conn, rst_id, "error", str(e)[:500])
      failed += 1
      continue

    elapsed = time.time() - t_start

    if result["source"] == "none":
      print(f"  No menu evidence found ({elapsed:.1f}s)")
      record_log(conn, rst_id, "no_menu")
      skipped_no_menu += 1
      continue

    if review_task:
      record_log(conn, rst_id, "category_review_pending")
      print(
        f"  Queued {review_task['category_count']} restaurant-level food categories "
        f"from {result['source']} evidence ({elapsed:.1f}s)"
      )
      queued_for_review += 1
      continue

    record_log(conn, rst_id, "done")
    print(f"  No new categories require review ({elapsed:.1f}s)")
    no_new_categories += 1

  conn.close()
  print(
    f"\nFinished. category_review_pending={queued_for_review}, "
    f"no_new_categories={no_new_categories}, no_menu={skipped_no_menu}, failed={failed}"
  )


if __name__ == "__main__":
  main()
