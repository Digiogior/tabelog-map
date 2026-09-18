"""Manual integration runner for the production menu pipeline.

This script makes live Tabelog, Mistral, and NVIDIA requests. Unit tests live in
test_menu_pipeline_unit.py and do not make network calls.
"""

import csv
import json
import os
import sys
import time

PROJECT_ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))
sys.path.insert(0, PROJECT_ROOT)

if __name__ == "__main__":
  from menu_scraper import scrape_menu

  csv_path = os.path.join(PROJECT_ROOT, "NagoyaRstUrls.csv")
  with open(csv_path, encoding="utf-8") as csv_file:
    urls = [row["url"] for row in csv.DictReader(csv_file)]

  test_urls = [url.replace("/tw/", "/") for url in urls[:6]]
  summary = []

  for url in test_urls:
    started_at = time.time()
    items = scrape_menu(url)
    elapsed = time.time() - started_at
    summary.append({"url": url, "items": len(items or []), "time_sec": round(elapsed, 1)})
    print(json.dumps(items, ensure_ascii=False, indent=2))

  print(json.dumps(summary, ensure_ascii=False, indent=2))
