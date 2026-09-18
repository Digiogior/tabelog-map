# tabelog-map

An interactive map of Japanese restaurants sourced from [Tabelog](https://tabelog.com/tw/), covering multiple cities across Japan. The scraper crawls restaurant data at prefecture scale, stores it in PostgreSQL with PostGIS, and renders nearby restaurants on a Mapbox map with category filtering, price info, and photo thumbnails.

**Current data:**

| Prefecture | Restaurants |
|---|---|
| Tokyo | ~132,000 |
| Osaka | ~57,000 |
| Aichi (Nagoya area) | ~16,000 |

---

## Architecture

```
Tabelog (tw)
  └─ Prefecture page          →  discover areas (A2701, A2702…)
       └─ Area pages          →  discover sub-areas + food-type categories
            └─ Listing pages  →  paginated restaurant URLs  →  {Prefecture}RstUrls.csv
                 └─ Detail pages  →  name, address, rating, price, photos, coords
                      └─ PostgreSQL  →  restaurants, categories, menu_items tables
```

**Key design choices:**
- Tabelog caps listing pages at 60 per URL. The scraper first splits by area and food type, then automatically crawls `sub-area × food type` when a listing reaches the cap.
- Transient collection failures use bounded exponential backoff and honor `Retry-After`. Exhausted listing pages are saved in `{Prefecture}RstUrls.failed-paths.csv` and resumed before other paths on the next run.
- URL collection and detail scraping are separate phases, both resumable.
- Detail scraping uses 10 concurrent workers with automatic 429 backoff.
- Scrape progress is tracked in the `scrape_progress` DB table — re-running any command resumes where it left off.

---

## Project Structure

```
tabelog-map/
├── cmd/
│   ├── api/main.go              ← API server entry point
│   └── scraper/main.go          ← Scraper entry point
├── internal/
│   ├── api/server.go            ← HTTP handlers
│   ├── service/restaurant.go    ← Business logic
│   ├── db/restaurant.go         ← SQL queries
│   └── scraper/
│       ├── crawler.go           ← Tabelog crawling (multi-city, concurrent)
│       └── progress.go          ← CSV-based progress (legacy)
├── map/
│   ├── index.html               ← Frontend
│   └── map.js                   ← Mapbox integration
├── models/restaurant.go         ← Restaurant struct
├── menu_scraper.py              ← OCR/VLM food-category classifier (optional)
├── {Prefecture}RstUrls.csv      ← Collected restaurant URLs per city
└── docker-compose.yml           ← PostgreSQL 18 + PostGIS
```

---

## Database Schema

| Table | Description |
|---|---|
| `restaurants` | Core table — name, address, rating, price range, coords, photos, city |
| `categories` | Distinct food-type categories (500+) |
| `restaurant_categories` | Many-to-many join |
| `menu_items` | Menu items extracted by VLM scraper |
| `menu_scrape_log` | Per-restaurant menu scrape status |
| `food_types` | Controlled multilingual food taxonomy |
| `food_category_review_*` | Restaurant-level category tasks, candidates, photos, and evidence |
| `restaurant_food_types` | Human-approved searchable restaurant food types |
| `food_taxonomy_proposals` | Pending reviewer proposals for taxonomy administrators |
| `scrape_progress` | Per-URL detail scrape status (pending / done / error) |
| `cities` | Registered prefectures with display name and map center |

---

## Requirements

- Go 1.24+
- Docker (PostgreSQL + PostGIS)
- Python 3.10+ and a NVIDIA Inference API key (optional, for menu scraping)

---

## Setup

### 1. Start the database

```bash
docker compose up -d
```

### 2. Collect restaurant URLs

```bash
go run cmd/scraper/main.go --prefecture tokyo --collect-urls
# → TokyoRstUrls.csv  (~82,000 unique URLs)

go run cmd/scraper/main.go --prefecture osaka --collect-urls
# → OsakaRstUrls.csv  (~58,000 unique URLs)

go run cmd/scraper/main.go --prefecture aichi --collect-urls
# → AichiRstUrls.csv
```

The `--prefecture` slug comes from the Tabelog URL: `https://tabelog.com/tw/{prefecture}/`.

Collection for a large city like Tokyo takes several hours due to polite rate limiting (5s per page, 3 concurrent workers).

### 3. Scrape restaurant details

```bash
export DATABASE_URL="postgres://postgres:password@localhost:5432/nagoya"

go run cmd/scraper/main.go --prefecture osaka
```

Scraping runs 10 concurrent workers. Progress is stored in the `scrape_progress` table — interrupting and re-running the command resumes automatically.

```bash
# Retry URLs that previously errored
go run cmd/scraper/main.go --prefecture osaka --retry-errors
```

**Scraper flags:**

| Flag | Default | Description |
|---|---|---|
| `--prefecture` | *(required)* | Prefecture slug, e.g. `tokyo`, `osaka`, `aichi` |
| `--urls-file` | `{Prefecture}RstUrls.csv` | Input CSV of restaurant URLs |
| `--db` | `DATABASE_URL` env or localhost | PostgreSQL DSN |
| `--workers` | `10` | Concurrent detail-scrape workers |
| `--collect-urls` | — | Run URL collection only, then exit |
| `--retry-errors` | — | Reset errored URLs back to pending |

### 4. Start the API server

```bash
DATABASE_URL="postgres://postgres:password@localhost:5432/nagoya" go run cmd/api/main.go
```

Listens on `:8080`.

### 5. Open the map

Open `map/index.html` in a browser. The map:
- Explains why location is useful before requesting browser permission
- Shows restaurants within 1.5km as clustered markers after the user shares a location or chooses an area
- Supports station, neighborhood, city, and address search plus a **Search this area** map fallback when location is blocked
- Tapping a marker opens a bottom sheet with name, address, rating, price range, photo thumbnails, and links to Tabelog and Google Maps
- Category chip bar along the top shows the 10 most common categories; tap **More** to search all 500+
- Swipe down or tap the map to dismiss the bottom sheet

---

## API Reference

### `GET /api/restaurants`

Returns restaurants within 1.5km of the given point.

| Parameter | Required | Description |
|---|---|---|
| `lat` | Yes | Latitude |
| `lng` | Yes | Longitude |
| `category` | No | Filter by category name |
| `prefecture` | No | Limit to one city, e.g. `tokyo` |

### `GET /api/categories`

Returns all category names sorted alphabetically.

### `GET /api/categories/top`

Returns the 10 categories with the most restaurants.

### `GET /api/food-types`

Returns the active controlled food taxonomy with English, Japanese, and
Traditional Chinese labels, search aliases, and parent metadata. Child labels
are displayed as paths such as `Dessert › Cake`.

### `GET /api/food-type-search?q=Salad`

Resolves a localized label, alias, or taxonomy slug and returns restaurants
whose human-approved menu categories match it. Parent searches recursively
include approved child categories, so a search for `Dessert` also finds a
restaurant tagged with `Cake`. Pending model suggestions are never returned.

---

## Food Category Pipeline (Optional)

`menu_scraper.py` classifies restaurant menu evidence into controlled,
searchable food types such as `Salad › Meat salad`, `Skewers › Yakitori`, and
`Fried food › Fried chicken`. It does not publish individual dish names or
prices. Drink-only menus are ignored.

Existing dish-review evidence is reused by default so previously paid OCR work
does not need to be repeated. When no reusable evidence exists, Mistral OCR 4
reads four overlapping photo tiles and Gemini 3.6 Flash maps exact evidence to
the taxonomy in `internal/db/food_taxonomy.json`.

**Setup:**
```bash
python -m venv .venv
.venv/bin/pip install -r requirements.txt
# Add MISTRAL_KEY and NVIDIA_Inference_Key to .env
```

**Run:**
```bash
# All pending restaurants
.venv/bin/python menu_scraper.py

# Split across multiple machines (e.g. 4 machines × 5000 restaurants)
.venv/bin/python menu_scraper.py --offset 0     --limit 5000
.venv/bin/python menu_scraper.py --offset 5000  --limit 5000

# Convert legacy photo/dish review evidence without rerunning OCR
.venv/bin/python menu_scraper.py --migrate-legacy-reviews

# Reclassify one restaurant and replace only its pending category review
.venv/bin/python menu_scraper.py \
  --restaurant-url https://tabelog.com/tw/aichi/A2301/A230108/23045697/ \
  --refresh-reviews

# Force the live tiled OCR path instead of reusing legacy evidence
MENU_MAX_PHOTOS=1 .venv/bin/python menu_scraper.py \
  --restaurant-url https://tabelog.com/tw/aichi/A2301/A230108/23045697/ \
  --refresh-reviews \
  --force-live-ocr
```

Classification results from all usable photos are aggregated into one pending
review task per restaurant. Refresh replaces only a pending task; completed
human decisions are never overwritten. The classifier chooses the most
specific supported child and uses a parent only when the evidence is too
ambiguous for a subtype. Redundant parent candidates are removed because
parent searches already include their descendants.

### Human food-category review

Start the API and open the review page:

```bash
go run ./cmd/api
# http://localhost:8080/review.html
```

The page shows all usable menu photos beside deduplicated food categories for
one restaurant. Every suggestion must be kept, changed to another existing
taxonomy category, or removed. Reviewers can add a missed existing category or
submit a pending taxonomy proposal. Saving publishes only confirmed categories
to `restaurant_food_types`.

Review queue endpoints:

- `GET /api/food-category-reviews/next`
- `GET /api/food-category-reviews/stats`
- `POST /api/food-category-reviews/{id}`

The original photo-level `menu_review_*` and `menu_items` tables remain as
migration and audit history.

---

## Adding a New City

```bash
# 1. Collect restaurant URLs
go run cmd/scraper/main.go --prefecture kyoto --collect-urls

# 2. Scrape details
go run cmd/scraper/main.go --prefecture kyoto

# 3. Register the city for the UI
psql $DATABASE_URL -c "
  INSERT INTO cities (prefecture, display_name, lat, lng)
  VALUES ('kyoto', 'Kyoto', 35.0116, 135.7681);
"
```

---

## Monitoring Scrape Progress

```sql
-- Overall status by city
SELECT r.city, sp.status, COUNT(*)
FROM scrape_progress sp
JOIN restaurants r ON r.url = sp.url
GROUP BY r.city, sp.status
ORDER BY r.city, sp.status;

-- Quick counts
SELECT status, COUNT(*) FROM scrape_progress GROUP BY status;
```

---

## Running Tests

```bash
go test ./...
```
