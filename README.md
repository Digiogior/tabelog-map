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
- Tabelog caps listing pages at 60 per URL. The scraper bypasses this by crawling each food-type category (sushi, yakiniku, ramen…) as a separate listing, then deduplicating URLs in memory.
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
├── menu_scraper.py              ← VLM-based menu item scraper (optional)
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
- Requests your location and shows restaurants within 1.5km as clustered markers
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

---

## Menu Scraper (Optional)

`menu_scraper.py` extracts menu items from restaurant photo pages using a VLM (Gemini Flash via NVIDIA Inference API). It tries HTML-based extraction first and falls back to image analysis.

**Setup:**
```bash
pip install -r requirements.txt
# Add NVIDIA_Inference_Key to .env
```

**Run:**
```bash
# All pending restaurants
python menu_scraper.py

# Target a specific city
python menu_scraper.py --prefecture osaka

# Split across multiple machines (e.g. 4 machines × 5000 restaurants)
python menu_scraper.py --offset 0     --limit 5000   # machine 1
python menu_scraper.py --offset 5000  --limit 5000   # machine 2
python menu_scraper.py --offset 10000 --limit 5000   # machine 3
python menu_scraper.py --offset 15000 --limit 5000   # machine 4
```

Menu search is available via `GET /api/menu-search?q=ビール` (trigram similarity).

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
