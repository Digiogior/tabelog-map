# tabelog-map

An interactive map of Japanese restaurants sourced from [Tabelog](https://tabelog.com/tw/), covering multiple cities across Japan. The scraper crawls restaurant data at prefecture scale, stores it in PostgreSQL with PostGIS for geo queries, and renders nearby restaurants as markers on a Mapbox map.

**Current data:**
| Prefecture | Restaurants |
|---|---|
| Tokyo | ~132,000 |
| Osaka | ~57,000 |
| Aichi (Nagoya + surroundings) | ~16,000+ |

---

## How it works

```
Tabelog (tw)
  └─ Prefecture page  →  fetchAreaPaths
       └─ Areas (A2701…)  →  fetchSubAreaPaths + fetchCategoryPaths
            └─ Restaurant listing pages (paginated, per category)
                 └─ Individual restaurant URLs  →  {Prefecture}RstUrls.csv
                      └─ Detail pages  →  PostgreSQL (restaurants table)
```

The scraper bypasses Tabelog's 60-page-per-listing cap by crawling each food-type category separately, then deduplicating across all paths.

---

## Project Structure

```
tabelog-map/
├── cmd/
│   ├── api/
│   │   └── main.go              ← API server entry point
│   └── scraper/
│       └── main.go              ← Scraper entry point (URL collection + detail scraping)
├── internal/
│   ├── api/
│   │   └── server.go            ← HTTP handlers
│   ├── service/
│   │   └── restaurant.go        ← Business logic
│   ├── db/
│   │   └── restaurant.go        ← SQL queries
│   └── scraper/
│       ├── crawler.go           ← Tabelog crawling logic (multi-city, concurrent)
│       └── progress.go          ← Crawl progress tracking
├── map/
│   ├── index.html               ← Frontend
│   └── map.js                   ← Mapbox map + API integration
├── models/
│   └── restaurant.go            ← Restaurant struct
├── menu_scraper.py              ← VLM-based menu item scraper (optional)
├── {Prefecture}RstUrls.csv      ← Collected restaurant URLs per city
└── docker-compose.yml           ← PostgreSQL + PostGIS
```

---

## Requirements

- Go 1.24+
- Docker (for PostgreSQL + PostGIS)
- Python 3.10+ (optional, for menu scraping)

---

## Setup

### 1. Start the database

```bash
docker compose up -d
```

### 2. Collect restaurant URLs for a city

```bash
go run cmd/scraper/main.go --prefecture tokyo --collect-urls
# Output: TokyoRstUrls.csv

go run cmd/scraper/main.go --prefecture osaka --collect-urls
# Output: OsakaRstUrls.csv

go run cmd/scraper/main.go --prefecture aichi --collect-urls
# Output: AichiRstUrls.csv
```

The `--prefecture` value is the slug from the Tabelog URL: `https://tabelog.com/tw/{prefecture}/`.

### 3. Scrape restaurant details into the database

```bash
go run cmd/scraper/main.go --prefecture osaka --db "postgres://postgres:password@localhost:5432/nagoya"
```

Progress is tracked in the `scrape_progress` table. Re-running resumes from where it left off. To retry failed URLs:

```bash
go run cmd/scraper/main.go --prefecture osaka --db "..." --retry-errors
```

**Scraper flags:**

| Flag | Default | Description |
|---|---|---|
| `--prefecture` | *(required)* | Prefecture slug, e.g. `tokyo`, `osaka`, `aichi` |
| `--urls-file` | `{Prefecture}RstUrls.csv` | Input CSV of restaurant URLs |
| `--db` | `DATABASE_URL` env or localhost | PostgreSQL DSN |
| `--workers` | `10` | Concurrent scrape workers |
| `--collect-urls` | false | Run URL collection only, then exit |
| `--retry-errors` | false | Retry previously failed URLs |

### 4. Start the API server

```bash
DATABASE_URL="postgres://postgres:password@localhost:5432/nagoya" go run cmd/api/main.go
```

The API listens on `:8080`.

### 5. Open the frontend

Open `map/index.html` in a browser. The map will:
- Request your current location
- Fetch restaurants within 1.5km and render them as clustered markers
- Show name, address, rating, price range, photos, and links to Tabelog and Google Maps on tap
- Filter by category via the chip bar (top categories shown inline; full list via "More")

---

## API

### `GET /api/restaurants?lat=&lng=&category=&prefecture=`

Returns restaurants within 1.5km of the given coordinates.

| Parameter | Required | Description |
|---|---|---|
| `lat` | Yes | Latitude |
| `lng` | Yes | Longitude |
| `category` | No | Filter by category name |
| `prefecture` | No | Limit to one city (e.g. `tokyo`) |

### `GET /api/categories`

Returns all category names.

### `GET /api/categories/top`

Returns the 10 most-represented categories by restaurant count.

---

## Adding a new city

```bash
# 1. Collect URLs
go run cmd/scraper/main.go --prefecture kyoto --collect-urls

# 2. Scrape details
go run cmd/scraper/main.go --prefecture kyoto --db "postgres://..."

# 3. Register in the cities table (for the city selector UI)
psql $DATABASE_URL -c "INSERT INTO cities (prefecture, display_name, lat, lng) VALUES ('kyoto', 'Kyoto', 35.0116, 135.7681);"
```

---

## Running Tests

```bash
go test ./...
```
