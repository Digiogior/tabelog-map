import os
import base64
import httpx
from dotenv import load_dotenv
from openai import OpenAI

load_dotenv(os.path.join(os.path.dirname(__file__), '..', '.env'))

client = OpenAI(
  base_url = "https://inference-api.nvidia.com/v1/",
  api_key = os.environ["NVIDIA_Inference_Key"]
)

PHOTOS = [
  "https://tblg.k-img.com/restaurant/images/Rvw/352359/640x640_rect_824eb73db154fb77b6a5d6d138be8ba9.jpg",
  "https://tblg.k-img.com/restaurant/images/Rvw/336265/640x640_rect_ab14fc91406e10aa1818ea601ca8fd62.jpg",
  "https://tblg.k-img.com/restaurant/images/Rvw/336265/640x640_rect_618ab1783fc64598c173ee90d660d650.jpg",
]

PROMPT = (
  "This is a Japanese restaurant menu photo. "
  "Extract all menu items and return as JSON array. "
  "Each item should have: name (Japanese text as-is), price (integer yen, null if not visible), description (if any). "
  "Return only the JSON array, no explanation."
)

MODELS = [
  "openai/openai/gpt-5-mini",
]

for i, url in enumerate(PHOTOS, 1):
  image_data = base64.standard_b64encode(httpx.get(url).content).decode("utf-8")

  for model in MODELS:
    print(f"\n{'='*60}")
    print(f"Photo {i} | Model: {model}")
    print('='*60)

    completion = client.chat.completions.create(
      model=model,
      messages=[{
        "role": "user",
        "content": [
          {"type": "image_url", "image_url": {"url": f"data:image/jpeg;base64,{image_data}"}},
          {"type": "text", "text": PROMPT}
        ]
      }],
      temperature=0.2,
      top_p=0.7,
      max_tokens=8192,
      stream=False
    )

    print(completion.choices[0].message.content)
