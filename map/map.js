mapboxgl.accessToken = window.MAPBOX_TOKEN;
const options = {
  enableHighAccuracy: true,
};

navigator.geolocation.getCurrentPosition(
  successLocation,
  errorLocation,
  options,
);

const userMarker = new mapboxgl.Marker({ color: "#007cbf" });
let currentPosition = null;
let selectedCategory = "";

function successLocation(pos) {
  const crd = pos.coords;
  console.log(crd.latitude);
  console.log(crd.longitude);
  setMap([crd.longitude, crd.latitude]);
}

function errorLocation() {
  const nagoyaLatitude = 35.1471154;
  const nagoyaLongitude = 136.9263111;
  setMap([nagoyaLongitude, nagoyaLatitude]);
}

function fetchAndRenderRestaurants(map, lng, lat) {
  const url = new URL("/api/restaurants", location.origin);
  url.searchParams.set("lat", lat);
  url.searchParams.set("lng", lng);
  if (selectedCategory) url.searchParams.set("category", selectedCategory);

  fetch(url)
    .then((res) => res.json())
    .then((restaurants) => {
      const geojson = {
        type: "FeatureCollection",
        features: restaurants.map((r) => ({
          type: "Feature",
          geometry: { type: "Point", coordinates: [r.Longitude, r.Latitude] },
          properties: {
            name: r.Name,
            address: r.Address,
            url: r.URL,
            rating: r.Rating,
            kids: r.Kids,
            categories: r.Categories ? r.Categories.join(", ") : "",
          },
        })),
      };

      if (map.getSource("restaurants")) {
        map.getSource("restaurants").setData(geojson);
      } else {
        map.addSource("restaurants", {
          type: "geojson",
          data: geojson,
          cluster: true,
          clusterMaxZoom: 15,
          clusterRadius: 50,
        });

        // Cluster bubble
        map.addLayer({
          id: "clusters",
          type: "circle",
          source: "restaurants",
          filter: ["has", "point_count"],
          paint: {
            "circle-color": [
              "step", ["get", "point_count"],
              "#f28cb1", 10,
              "#e74c3c", 30,
              "#c0392b"
            ],
            "circle-radius": [
              "step", ["get", "point_count"],
              22, 10,
              30, 30,
              38
            ],
            "circle-stroke-width": 2,
            "circle-stroke-color": "#fff",
          },
        });

        // Cluster count label
        map.addLayer({
          id: "cluster-count",
          type: "symbol",
          source: "restaurants",
          filter: ["has", "point_count"],
          layout: {
            "text-field": "{point_count_abbreviated}",
            "text-font": ["DIN Offc Pro Medium", "Arial Unicode MS Bold"],
            "text-size": 14,
          },
          paint: { "text-color": "#fff" },
        });

        // Individual restaurant point
        map.addLayer({
          id: "restaurants",
          type: "circle",
          source: "restaurants",
          filter: ["!", ["has", "point_count"]],
          paint: {
            "circle-radius": 10,
            "circle-color": "#e74c3c",
            "circle-stroke-width": 2,
            "circle-stroke-color": "#fff",
          },
        });

        // Tap cluster → zoom in
        map.on("click", "clusters", (e) => {
          const features = map.queryRenderedFeatures(e.point, { layers: ["clusters"] });
          const clusterId = features[0].properties.cluster_id;
          map.getSource("restaurants").getClusterExpansionZoom(clusterId, (err, zoom) => {
            if (err) return;
            map.easeTo({ center: features[0].geometry.coordinates, zoom });
          });
        });

        // Tap individual restaurant → bottom sheet
        map.on("click", "restaurants", (e) => {
          e.originalEvent.stopPropagation();
          const p = e.features[0].properties;
          showBottomSheet(p);
        });

        map.on("mouseenter", "clusters", () => { map.getCanvas().style.cursor = "pointer"; });
        map.on("mouseleave", "clusters", () => { map.getCanvas().style.cursor = ""; });
        map.on("mouseenter", "restaurants", () => { map.getCanvas().style.cursor = "pointer"; });
        map.on("mouseleave", "restaurants", () => { map.getCanvas().style.cursor = ""; });
      }
    })
    .catch((err) => console.error("failed to fetch restaurants:", err));
}

function showBottomSheet(p) {
  let sheet = document.getElementById("bottom-sheet");
  if (!sheet) {
    sheet = document.createElement("div");
    sheet.id = "bottom-sheet";
    document.body.appendChild(sheet);

    sheet.addEventListener("click", (e) => e.stopPropagation());
    document.getElementById("map").addEventListener("click", () => {
      sheet.classList.remove("open");
    });
  }

  const rating = p.rating > 0 ? `⭐ ${p.rating}` : "No rating";
  const cats = p.categories ? `<div class="bs-categories">${p.categories}</div>` : "";
  sheet.innerHTML = `
    <div class="bs-handle"></div>
    <div class="bs-name"><a href="${p.url}" target="_blank">${p.name}</a></div>
    <div class="bs-address">${p.address}</div>
    ${cats}
    <div class="bs-meta">${rating}${p.kids ? ` &nbsp;·&nbsp; 👶 ${p.kids}` : ""}</div>
  `;
  sheet.classList.add("open");
}

function buildCategoryFilter(map) {
  fetch("/api/categories")
    .then((res) => res.json())
    .then((categories) => {
      const container = document.createElement("div");
      container.className = "category-filter";

      const input = document.createElement("input");
      input.type = "text";
      input.placeholder = "Filter by category...";

      const list = document.createElement("div");
      list.className = "autocomplete-list";
      list.style.display = "none";

      let activeIndex = -1;

      function getMatches(query) {
        if (!query) return ["全部"];
        return ["全部", ...categories.filter((c) =>
          c.toLowerCase().includes(query.toLowerCase())
        )];
      }

      function renderList(matches) {
        list.innerHTML = "";
        activeIndex = -1;
        if (matches.length === 0) {
          list.style.display = "none";
          return;
        }
        matches.forEach((cat) => {
          const item = document.createElement("div");
          item.className = "autocomplete-item";
          item.textContent = cat;
          item.addEventListener("mousedown", () => {
            selectCategory(cat);
          });
          list.appendChild(item);
        });
        list.style.display = "block";
      }

      function selectCategory(cat) {
        if (cat === "全部") {
          input.value = "";
          selectedCategory = "";
        } else {
          input.value = cat;
          selectedCategory = cat;
        }
        list.style.display = "none";
        if (currentPosition) {
          fetchAndRenderRestaurants(map, currentPosition[0], currentPosition[1]);
        }
      }

      input.addEventListener("input", () => {
        renderList(getMatches(input.value));
      });

      input.addEventListener("keydown", (e) => {
        const items = list.querySelectorAll(".autocomplete-item");
        if (e.key === "ArrowDown") {
          activeIndex = Math.min(activeIndex + 1, items.length - 1);
        } else if (e.key === "ArrowUp") {
          activeIndex = Math.max(activeIndex - 1, -1);
        } else if (e.key === "Enter") {
          if (activeIndex >= 0 && items[activeIndex]) {
            selectCategory(items[activeIndex].textContent);
          } else {
            selectedCategory = input.value.trim();
            list.style.display = "none";
            if (currentPosition) {
              fetchAndRenderRestaurants(map, currentPosition[0], currentPosition[1]);
            }
          }
          return;
        } else if (e.key === "Escape") {
          list.style.display = "none";
          return;
        }
        items.forEach((el, i) => {
          el.classList.toggle("active", i === activeIndex);
        });
      });

      input.addEventListener("blur", () => {
        setTimeout(() => { list.style.display = "none"; }, 150);
      });

      input.addEventListener("focus", () => {
        renderList(getMatches(input.value));
      });

      container.appendChild(input);
      container.appendChild(list);
      document.getElementById("map").appendChild(container);
    })
    .catch((err) => console.error("failed to fetch categories:", err));
}

function setMap(center) {
  currentPosition = center;

  const map = new mapboxgl.Map({
    container: "map",
    center: center,
    zoom: 18,
  });

  userMarker.setLngLat(center).addTo(map);
  map.on("load", () => {
    fetchAndRenderRestaurants(map, center[0], center[1]);
  });
  buildCategoryFilter(map);

  const geolocate = new mapboxgl.GeolocateControl({
    positionOptions: { enableHighAccuracy: true },
    trackUserLocation: true,
    showUserHeading: true,
  });

  map.addControl(geolocate);
  geolocate.on("geolocate", (e) => {
    const lng = e.coords.longitude;
    const lat = e.coords.latitude;
    const position = [lng, lat];

    currentPosition = position;
    userMarker.setLngLat(position).addTo(map);
    fetchAndRenderRestaurants(map, lng, lat);
  });
}
