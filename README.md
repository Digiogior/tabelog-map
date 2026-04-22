# tabelog-map

An interactive map that displays restaurants in the Nagoya/Aichi area sourced from [Tabelog](https://tabelog.com). It crawls restaurant data, stores it in PostgreSQL with PostGIS, and renders nearby restaurants as markers on a Mapbox map.

---

## Project Structure

```
tabelog-map/
├── cmd/
│   ├── api/
│   │   └── main.go         ← API server entry point
│   └── scraper/
│       └── main.go         ← Scraper entry point
├── internal/
│   ├── api/
│   │   └── server.go       ← HTTP handlers
│   ├── service/
│   │   └── restaurant.go   ← Business logic
│   ├── db/
│   │   └── restaurant.go   ← SQL queries
│   └── scraper/
│       ├── crawler.go      ← Tabelog crawling logic
│       └── progress.go     ← Crawl progress tracking
├── map/
│   ├── index.html          ← Frontend
│   └── map.js              ← Mapbox map + API integration
├── models/
│   └── restaurant.go       ← Restaurant struct
├── NagoyaRstUrls.csv       ← Collected restaurant URLs
├── tabelogUrls.csv         ← Listing page URLs to crawl
├── crawl_progress.csv      ← Crawl progress tracker (auto-created)
└── docker-compose.yml      ← PostgreSQL + PostGIS
```

---

## Requirements

- Go 1.24+
- Docker

---

## Setup

### 1. Start the database

```bash
docker compose up -d
```

### 2. Collect restaurant URLs

**Option A** — Crawl all sub-areas of a city:
```bash
# Uncomment scraper.FetchRstUrls(...) in cmd/scraper/main.go
go run cmd/scraper/main.go
```

**Option B** — Crawl from a custom list of listing page URLs in `tabelogUrls.csv`:
```bash
# Uncomment scraper.FetchRstUrlsFromCSV(...) in cmd/scraper/main.go
go run cmd/scraper/main.go
```

`tabelogUrls.csv` format:
```
urls
https://tabelog.com/tw/aichi/A2301/rstLst/?SrtT=rt
https://tabelog.com/tw/aichi/A2301/rstLst/?SrtT=inbound_access
```

Collected URLs are saved to `NagoyaRstUrls.csv`.

### 3. Crawl restaurant info and populate the database

```bash
go run cmd/scraper/main.go
```

Progress is tracked in `crawl_progress.csv`. If the process is interrupted, re-running the command will **resume from where it left off**, skipping already completed URLs.

To retry URLs that previously failed:
```bash
go run cmd/scraper/main.go -retry-errors
```

### 4. Start the API server

```bash
go run cmd/api/main.go
```

The API listens on `:8080`.

### 5. Open the frontend

Open `map/index.html` in a browser. The map will:
- Request your current location
- Fetch restaurants within 6km and render them as markers
- Show name, address, rating, kids info, and a link to Tabelog on marker click
- Allow filtering by category via the dropdown

---

## API

### `GET /api/restaurants?lat=&lng=&category=`

Returns restaurants within 6km of the given coordinates.

| Parameter | Required | Description |
|---|---|---|
| `lat` | Yes | Latitude |
| `lng` | Yes | Longitude |
| `category` | No | Filter by category name |

### `GET /api/categories`

Returns all available restaurant categories.

---

## Running Tests

```bash
go test ./...
```
