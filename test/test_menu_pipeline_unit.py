import os
import sys
import types
import unittest
from io import BytesIO
from types import SimpleNamespace
from unittest.mock import MagicMock, patch

from PIL import Image

PROJECT_ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))
sys.path.insert(0, PROJECT_ROOT)

try:
  import psycopg2  # noqa: F401
except ModuleNotFoundError:
  psycopg2_module = types.ModuleType("psycopg2")
  psycopg2_extras_module = types.ModuleType("psycopg2.extras")
  psycopg2_module.extras = psycopg2_extras_module
  sys.modules["psycopg2"] = psycopg2_module
  sys.modules["psycopg2.extras"] = psycopg2_extras_module

import menu_scraper


class MenuPipelineTests(unittest.TestCase):
  def test_controlled_taxonomy_contains_searchable_salad_category(self):
    self.assertGreaterEqual(len(menu_scraper.FOOD_TAXONOMY), 70)
    self.assertEqual(menu_scraper.FOOD_TYPE_BY_SLUG["salad"]["name_ja"], "サラダ")

  def test_taxonomy_has_detailed_dessert_hierarchy(self):
    expected_children = {
      "japanese_sweets", "cake", "pudding", "parfait", "ice_cream",
      "crepe_pancake", "baked_sweets", "fruit",
    }
    actual_children = {
      item["slug"]
      for item in menu_scraper.FOOD_TAXONOMY
      if item.get("parent_slug") == "dessert"
    }
    self.assertEqual(actual_children, expected_children)
    self.assertEqual(menu_scraper.food_type_ancestors("cake"), ["dessert"])
    self.assertIn("Dessert > Cake", menu_scraper.FOOD_TAXONOMY_PROMPT)

  def test_provider_configuration(self):
    self.assertEqual(menu_scraper.NVIDIA_BASE_URL, "https://inference-api.nvidia.com/v1")
    self.assertEqual(menu_scraper.MISTRAL_OCR_MODEL, "mistral-ocr-4-0")
    self.assertEqual(menu_scraper.MISTRAL_OCR_URL, "https://api.mistral.ai/v1/ocr")
    self.assertEqual(
      menu_scraper.NVIDIA_BASE_URL + "/chat/completions",
      "https://inference-api.nvidia.com/v1/chat/completions",
    )
    self.assertEqual(menu_scraper.VLM_MODEL, "gcp/google/gemini-3.6-flash")

  def test_tabelog_thumbnail_url_is_upgraded_to_original(self):
    thumbnail = (
      "https://tblg.k-img.com/restaurant/images/Rvw/357954/"
      "640x640_rect_c7bd0853e5ef85912e2bd85995fd5356.jpg?token=1"
    )
    self.assertEqual(
      menu_scraper.original_tabelog_image_url(thumbnail),
      "https://tblg.k-img.com/restaurant/images/Rvw/357954/"
      "c7bd0853e5ef85912e2bd85995fd5356.jpg?token=1",
    )

  def test_dense_menu_image_is_split_into_four_overlapping_tiles(self):
    image = Image.new("RGB", (768, 1024), "white")
    encoded = BytesIO()
    image.save(encoded, format="JPEG")

    tiles = menu_scraper.build_ocr_tiles(encoded.getvalue(), "image/jpeg", grid=2)

    self.assertEqual([tile["id"] for tile in tiles], ["t0_0", "t0_1", "t1_0", "t1_1"])
    self.assertTrue(all(tile["mime_type"] == "image/jpeg" for tile in tiles))
    dimensions = [Image.open(BytesIO(tile["data"])).size for tile in tiles]
    self.assertTrue(all(width > 384 and height > 512 for width, height in dimensions))

  @patch("menu_scraper.httpx.post")
  def test_mistral_ocr_request_uses_configured_key_and_ocr4(self, post):
    response = SimpleNamespace(
      status_code=200,
      headers={},
      raise_for_status=lambda: None,
      json=lambda: {"pages": []},
    )
    post.return_value = response

    with patch.dict(os.environ, {"MISTRAL_KEY": "test-key"}, clear=False):
      result = menu_scraper.call_ocr(b"image", "image/jpeg", max_retries=1)

    self.assertEqual(result, {"pages": []})
    _, kwargs = post.call_args
    self.assertEqual(post.call_args.args[0], menu_scraper.MISTRAL_OCR_URL)
    self.assertEqual(kwargs["headers"]["Authorization"], "Bearer test-key")
    self.assertEqual(kwargs["json"]["model"], "mistral-ocr-4-0")
    self.assertTrue(kwargs["json"]["include_blocks"])
    self.assertEqual(kwargs["json"]["confidence_scores_granularity"], "word")

  def test_detects_drink_only_page_title_but_not_mixed_section(self):
    self.assertTrue(menu_scraper.is_drink_only_menu([
      {"content": "![menu](image.jpg)"},
      {"content": "# DRINK MENU"},
      {"content": "日本酒・焼酎・ソフトドリンク"},
    ]))
    self.assertFalse(menu_scraper.is_drink_only_menu([
      {"content": "# DINNER MENU"},
      {"content": "焼鳥"},
      {"content": "# DRINK MENU"},
    ]))

  def test_validation_rejects_names_without_cited_ocr_evidence(self):
    blocks = [
      {"id": "p0_b0", "type": "text", "content": "特上カルビ 1,800円"},
      {"id": "p0_b1", "type": "title", "content": "焼肉メニュー"},
    ]
    candidates = [
      {"name": "特上カルビ", "evidence_block_ids": ["p0_b0"]},
      {"name": "和牛タン", "evidence_block_ids": ["p0_b0"]},
      {"name": "焼肉メニュー", "evidence_block_ids": ["missing"]},
    ]

    self.assertEqual(
      menu_scraper.validate_item_candidates(candidates, blocks),
      [{"name": "特上カルビ", "evidence_block_ids": ["p0_b0"]}],
    )

  def test_validation_cleans_bullets_and_excludes_serving_options(self):
    blocks = [
      {"id": "p0_b0", "type": "text", "content": "※ 下記から飲み方をお選び下さい"},
      {"id": "p0_b1", "type": "text", "content": "・ロック set 110(120)"},
      {"id": "p0_b2", "type": "title", "content": "# 焼酎"},
      {"id": "p0_b3", "type": "text", "content": "・麦 飲みきり ボトル (600 ml) 2182(2400)"},
    ]
    candidates = [
      {"name": "・ロック set", "evidence_block_ids": ["p0_b1"]},
      {"name": "・麦 飲みきり ボトル (600 ml)", "evidence_block_ids": ["p0_b3"]},
    ]

    self.assertEqual(
      menu_scraper.validate_item_candidates(candidates, blocks),
      [{"name": "麦 飲みきり ボトル", "evidence_block_ids": ["p0_b3"]}],
    )

  def test_estimates_priced_rows_without_counting_headings(self):
    blocks = [{
      "id": "p0_b0",
      "type": "text",
      "content": "# DRINK MENU\n・コーラ 319(350)\n・緑茶 319(350)",
    }]
    self.assertEqual(menu_scraper.estimate_priced_menu_rows(blocks), 2)

  @patch("menu_scraper.call_vlm")
  def test_low_recall_classification_retries_once(self, call_vlm):
    blocks = [{
      "id": "p0_b0",
      "type": "text",
      "content": "コーラ 319(350)\n緑茶 319(350)\nカルピス 319(350)\nウーロン茶 319(350)",
    }]
    call_vlm.side_effect = [
      SimpleNamespace(choices=[SimpleNamespace(message=SimpleNamespace(content="[]"))]),
      SimpleNamespace(choices=[SimpleNamespace(message=SimpleNamespace(content=(
        '[{"name":"コーラ","evidence_block_ids":["p0_b0"]},'
        '{"name":"緑茶","evidence_block_ids":["p0_b0"]}]'
      )))]),
    ]

    items = menu_scraper.classify_ocr_blocks(b"image", "image/jpeg", blocks)

    self.assertEqual(
      items,
      [
        {"name": "コーラ", "evidence_block_ids": ["p0_b0"]},
        {"name": "緑茶", "evidence_block_ids": ["p0_b0"]},
      ],
    )
    self.assertEqual(call_vlm.call_count, 2)

  def test_merge_is_exact_and_keeps_distinct_variants(self):
    items = [
      {"name": "カルビ"},
      {"name": "上カルビ"},
      {"name": "特上カルビ"},
      {"name": " 特上カルビ "},
    ]
    self.assertEqual(
      menu_scraper.merge_items(items),
      [{"name": "カルビ"}, {"name": "上カルビ"}, {"name": "特上カルビ"}],
    )

  def test_review_merge_keeps_evidence_and_deduplicates_names(self):
    items = [
      {"name": "タン塩", "evidence_block_ids": ["p0_b1"]},
      {"name": " タン塩 ", "evidence_block_ids": ["p0_b1", "p0_b2"]},
    ]
    self.assertEqual(
      menu_scraper.merge_review_items(items),
      [{"name": "タン塩", "evidence_block_ids": ["p0_b1", "p0_b2"]}],
    )

  def test_category_validation_requires_taxonomy_and_exact_evidence(self):
    blocks = [{
      "id": "p0_b0",
      "type": "text",
      "content": "バンバンジーサラダ 680円\n鶏の唐揚げ 720円",
    }]
    candidates = [
      {
        "food_type": "salad",
        "evidence": [{"text": "バンバンジーサラダ", "block_id": "p0_b0"}],
      },
      {
        "food_type": "fried_chicken",
        "evidence": [{"text": "幻の唐揚げ", "block_id": "p0_b0"}],
      },
      {
        "food_type": "made_up_type",
        "evidence": [{"text": "鶏の唐揚げ", "block_id": "p0_b0"}],
      },
    ]

    self.assertEqual(
      menu_scraper.validate_category_candidates(candidates, blocks),
      [{
        "food_type": "salad",
        "evidence": [{
          "text": "バンバンジーサラダ",
          "image_url": "",
          "source_page_url": "",
          "block_ids": ["p0_b0"],
        }],
      }],
    )

  def test_category_merge_deduplicates_type_and_retains_photo_evidence(self):
    categories = menu_scraper.merge_category_candidates([
      {
        "food_type": "salad",
        "evidence": [{"text": "野菜サラダ", "image_url": "one.jpg", "block_ids": ["a"]}],
      },
      {
        "food_type": "salad",
        "evidence": [{"text": "ポテトサラダ", "image_url": "two.jpg", "block_ids": ["b"]}],
      },
    ])

    self.assertEqual(len(categories), 1)
    self.assertEqual(categories[0]["food_type"], "salad")
    self.assertEqual(
      [evidence["image_url"] for evidence in categories[0]["evidence"]],
      ["one.jpg", "two.jpg"],
    )

  def test_category_merge_keeps_specific_child_and_drops_parent(self):
    categories = menu_scraper.merge_category_candidates([
      {
        "food_type": "dessert",
        "evidence": [{"text": "デザート", "block_ids": ["a"]}],
      },
      {
        "food_type": "cake",
        "evidence": [{"text": "チーズケーキ", "block_ids": ["b"]}],
      },
    ])

    self.assertEqual([category["food_type"] for category in categories], ["cake"])

  @patch("menu_scraper.classify_category_blocks")
  def test_legacy_classification_preserves_original_evidence(self, classify):
    classify.return_value = [{
      "food_type": "salad",
      "evidence": [{"text": "野菜サラダ", "block_ids": ["legacy_7"]}],
    }]
    categories = menu_scraper.classify_legacy_menu_evidence([{
      "id": 7,
      "image_url": "https://example.com/menu.jpg",
      "source_page_url": "https://example.com/menu/",
      "text": "野菜サラダ",
      "block_ids": ["t0_0_p0_b3"],
    }])

    self.assertEqual(categories[0]["evidence"][0], {
      "text": "野菜サラダ",
      "image_url": "https://example.com/menu.jpg",
      "source_page_url": "https://example.com/menu/",
      "block_ids": ["t0_0_p0_b3"],
    })

  def test_category_review_save_queues_restaurant_level_candidates(self):
    connection = MagicMock()
    cursor = connection.cursor.return_value.__enter__.return_value
    cursor.fetchone.side_effect = [(12,), (55,)]
    cursor.fetchall.return_value = []

    queued = menu_scraper.save_food_category_review_task(
      connection,
      573,
      [{
        "food_type": "salad",
        "evidence": [{
          "text": "野菜サラダ",
          "image_url": "https://example.com/menu.jpg",
          "source_page_url": "https://example.com/menu/",
          "block_ids": ["p0_b0"],
        }],
      }],
      refresh_pending=True,
    )

    self.assertEqual(queued, {"task_id": 12, "category_count": 1})
    connection.commit.assert_called_once()

  @patch("menu_scraper.fetch_photo_urls")
  @patch("menu_scraper.extract_html_menu_items")
  def test_scrape_menu_reads_food_html_only(self, extract_html, fetch_photos):
    extract_html.return_value = [{"name": "親子丼"}]

    items = menu_scraper.scrape_menu("https://tabelog.com/tw/aichi/example")

    self.assertEqual(items, [{"name": "親子丼"}])
    self.assertEqual(extract_html.call_count, 1)
    self.assertEqual(
      extract_html.call_args_list[0].args[0],
      "https://tabelog.com/tw/aichi/example/dtlmenu/",
    )
    fetch_photos.assert_not_called()

  @patch("menu_scraper.validate_vlm_access")
  @patch("menu_scraper.extract_items_from_photo")
  @patch("menu_scraper.fetch_photo_urls")
  @patch("menu_scraper.extract_html_menu_items")
  def test_photo_candidates_are_sent_to_review_callback(
    self, extract_html, fetch_photos, extract_photo, validate_access
  ):
    extract_html.return_value = []
    fetch_photos.return_value = ["https://example.com/menu.jpg"]
    extract_photo.return_value = [
      {"name": "タン塩", "evidence_block_ids": ["p0_b1"]}
    ]
    queued = []

    result = menu_scraper.scrape_menu_result(
      "https://tabelog.com/tw/aichi/example",
      on_photo_result=lambda image, items, page: queued.append((image, items, page)),
    )

    self.assertEqual(result, {"source": "photo", "items": [{"name": "タン塩"}]})
    self.assertEqual(
      queued,
      [(
        "https://example.com/menu.jpg",
        [{"name": "タン塩", "evidence_block_ids": ["p0_b1"]}],
        "https://tabelog.com/tw/aichi/example/dtlmenu/photo/",
      )],
    )
    validate_access.assert_not_called()

  @patch("menu_scraper.extract_items_from_photo")
  @patch("menu_scraper.fetch_photo_urls")
  @patch("menu_scraper.extract_html_menu_items")
  def test_empty_photo_result_is_sent_to_review_callback_for_refresh_cleanup(
    self, extract_html, fetch_photos, extract_photo
  ):
    extract_html.return_value = []
    fetch_photos.return_value = ["https://example.com/drinks.jpg"]
    extract_photo.return_value = []
    refreshed = []

    result = menu_scraper.scrape_menu_result(
      "https://tabelog.com/tw/aichi/example",
      on_photo_result=lambda image, items, page: refreshed.append((image, items, page)),
    )

    self.assertEqual(result, {"source": "photo", "items": []})
    self.assertEqual(
      refreshed,
      [(
        "https://example.com/drinks.jpg",
        [],
        "https://tabelog.com/tw/aichi/example/dtlmenu/photo/",
      )],
    )

  @patch("menu_scraper.time.sleep")
  @patch("menu_scraper.requests.get")
  def test_fetch_html_does_not_treat_forbidden_as_no_menu(self, get, _sleep):
    get.return_value = SimpleNamespace(status_code=403, headers={})

    with self.assertRaisesRegex(RuntimeError, "HTTP 403"):
      menu_scraper.fetch_html("https://tabelog.com/aichi/example/dtlmenu/", retries=1)

  @patch("menu_scraper.call_vlm")
  def test_vlm_access_is_validated_only_once(self, call_vlm):
    previous = menu_scraper._vlm_access_validated
    menu_scraper._vlm_access_validated = False
    try:
      menu_scraper.validate_vlm_access()
      menu_scraper.validate_vlm_access()
    finally:
      menu_scraper._vlm_access_validated = previous

    call_vlm.assert_called_once()

  @patch("menu_scraper.validate_vlm_access")
  @patch("menu_scraper.call_vlm")
  @patch("menu_scraper.call_ocr")
  @patch("menu_scraper.fetch_image_with_retry")
  def test_drink_photo_stops_after_first_ocr_tile(
    self, fetch_image, call_ocr, call_vlm, validate_access
  ):
    fetch_image.return_value = (b"jpeg-data", "image/jpeg")
    call_ocr.return_value = {
      "pages": [{
        "markdown": "# DRINK MENU\nビール",
        "blocks": [
          {"type": "title", "content": "# DRINK MENU"},
          {"type": "text", "content": "ビール 500円"},
        ],
      }]
    }

    items = menu_scraper.extract_items_from_photo("https://example.com/drinks.jpg")

    self.assertEqual(items, [])
    call_ocr.assert_called_once_with(b"jpeg-data", "image/jpeg")
    call_vlm.assert_not_called()
    validate_access.assert_not_called()

  @patch("menu_scraper.call_vlm")
  @patch("menu_scraper.call_ocr")
  @patch("menu_scraper.fetch_image_with_retry")
  def test_category_pipeline_stops_drink_photo_before_gemini(
    self, fetch_image, call_ocr, call_vlm
  ):
    fetch_image.return_value = (b"jpeg-data", "image/jpeg")
    call_ocr.return_value = {
      "pages": [{
        "markdown": "# DRINK MENU\nビール",
        "blocks": [
          {"type": "title", "content": "# DRINK MENU"},
          {"type": "text", "content": "ビール 500円"},
        ],
      }]
    }

    categories = menu_scraper.extract_categories_from_photo(
      "https://example.com/drinks.jpg"
    )

    self.assertEqual(categories, {
      "categories": [],
      "image_url": "https://example.com/drinks.jpg",
      "drink_only": True,
    })
    call_ocr.assert_called_once_with(b"jpeg-data", "image/jpeg")
    call_vlm.assert_not_called()

  @patch("menu_scraper.validate_vlm_access")
  @patch("menu_scraper.call_vlm")
  @patch("menu_scraper.call_ocr")
  @patch("menu_scraper.fetch_image_with_retry")
  def test_photo_pipeline_requires_ocr_evidence(
    self, fetch_image, call_ocr, call_vlm, validate_access
  ):
    fetch_image.return_value = (b"jpeg-data", "image/jpeg")
    call_ocr.return_value = {
      "pages": [{
        "markdown": "特上カルビ 1,800円",
        "blocks": [{"type": "text", "content": "特上カルビ 1,800円"}],
      }]
    }
    call_vlm.return_value = SimpleNamespace(
      choices=[SimpleNamespace(message=SimpleNamespace(content=(
        '[{"name":"特上カルビ","evidence_block_ids":["p0_b0"]},'
        '{"name":"幻のタン","evidence_block_ids":["p0_b0"]}]'
      )))]
    )

    items = menu_scraper.extract_items_from_photo("https://example.com/menu.jpg")

    self.assertEqual(
      items,
      [{"name": "特上カルビ", "evidence_block_ids": ["p0_b0"]}],
    )
    call_ocr.assert_called_once_with(b"jpeg-data", "image/jpeg")
    validate_access.assert_called_once()


if __name__ == "__main__":
  unittest.main()
