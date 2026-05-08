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
let selectedMarker = null;

function deselectRestaurant() {
  const sheet = document.getElementById("bottom-sheet");
  if (sheet) {
    sheet.classList.remove("open", "bounce");
    sheet.style.animation = "";
  }
  if (selectedMarker) { selectedMarker.remove(); selectedMarker = null; }
}

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
  const url = new URL("/api/restaurants", window.API_BASE || location.origin);
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
            photos: r.Photos ? JSON.stringify(r.Photos.slice(0, 3)) : "[]",
            lat: r.Latitude,
            lng: r.Longitude,
            lunchMinPrice: r.LunchMinPrice,
            lunchMaxPrice: r.LunchMaxPrice,
            dinnerMinPrice: r.DinnerMinPrice,
            dinnerMaxPrice: r.DinnerMaxPrice,
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
          const feature = e.features[0];
          const p = feature.properties;

          deselectRestaurant();
          const el = document.createElement("div");
          el.className = "selected-marker";
          el.addEventListener("click", (e) => e.stopPropagation());
          selectedMarker = new mapboxgl.Marker({ element: el, anchor: "center" })
            .setLngLat(feature.geometry.coordinates)
            .addTo(map);

          showBottomSheet(p);
        });

        map.on("mouseenter", "clusters", () => { map.getCanvas().style.cursor = "pointer"; });
        map.on("mouseleave", "clusters", () => { map.getCanvas().style.cursor = ""; });
        map.on("mouseenter", "restaurants", () => { map.getCanvas().style.cursor = "pointer"; });
        map.on("mouseleave", "restaurants", () => { map.getCanvas().style.cursor = ""; });

        // Tap empty map → deselect
        map.on("click", (e) => {
          const hit = map.queryRenderedFeatures(e.point, { layers: ["restaurants", "clusters"] });
          if (hit.length === 0) deselectRestaurant();
        });
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

    let dragStartY = 0;
    let dragging = false;

    sheet.addEventListener("touchstart", (e) => {
      const sheetTop = sheet.getBoundingClientRect().top;
      if (e.touches[0].clientY - sheetTop > 30) return;
      dragStartY = e.touches[0].clientY;
      dragging = true;
      sheet.classList.remove("bounce");
      sheet.style.animation = "none";
      sheet.style.transition = "none";
    }, { passive: true });

    sheet.addEventListener("touchmove", (e) => {
      if (!dragging) return;
      const dy = Math.max(0, e.touches[0].clientY - dragStartY);
      sheet.style.transform = `translateY(${dy}px)`;
    }, { passive: true });

    sheet.addEventListener("touchend", (e) => {
      if (!dragging) return;
      dragging = false;
      sheet.style.animation = "";
      sheet.style.transition = "";
      const dy = e.changedTouches[0].clientY - dragStartY;
      if (dy > 80) {
        deselectRestaurant();
      }
      sheet.style.transform = "";
    });
  }

  function priceRange(min, max) {
    if (!min && !max) return null;
    const fmt = (v) => v ? `¥${v.toLocaleString()}` : null;
    if (min && max) return `${fmt(min)} ~ ${fmt(max)}`;
    return fmt(min || max);
  }
  const lunchPrice = priceRange(p.lunchMinPrice, p.lunchMaxPrice);
  const dinnerPrice = priceRange(p.dinnerMinPrice, p.dinnerMaxPrice);
  const priceHtml = (lunchPrice || dinnerPrice) ? `
    <div class="bs-prices">
      ${lunchPrice ? `<span class="bs-price-item">🍱 Lunch: ${lunchPrice}</span>` : ""}
      ${dinnerPrice ? `<span class="bs-price-item">🍽 Dinner: ${dinnerPrice}</span>` : ""}
    </div>` : "";
  const rating = p.rating > 0 ? `⭐ ${p.rating}` : "No rating";
  const cats = p.categories ? `<div class="bs-categories">${p.categories}</div>` : "";
  const photos = JSON.parse(p.photos || "[]");
  const photoHtml = photos.length > 0 ? `
    <div class="bs-photos">
      <img class="bs-photo-main" src="${photos[0]}" alt="" loading="lazy">
      ${photos.length > 1 ? `<div class="bs-photos-row">
        ${photos.slice(1).map((u) => `<img class="bs-photo-thumb" src="${u}" alt="" loading="lazy">`).join("")}
      </div>` : ""}
    </div>` : "";
  const gmapUrl = `https://www.google.com/maps/search/?api=1&query=${encodeURIComponent(`${p.name} ${p.lat},${p.lng}`)}`;
  sheet.innerHTML = `
    <div class="bs-handle"></div>
    <div class="bs-header">
      <div class="bs-info">
        <div class="bs-name">${p.name}</div>
        <div class="bs-address">${p.address}</div>
        ${cats}
        ${priceHtml}
        <div class="bs-meta">${rating}${p.kids ? ` &nbsp;·&nbsp; 👶 ${p.kids}` : ""}</div>
      </div>
      ${photoHtml}
    </div>
    <div class="bs-actions">
      <a href="${gmapUrl}" target="_blank" class="bs-action-btn">📍 Google Maps</a>
      <a href="${p.url}" target="_blank" class="bs-action-btn">🍽 Tabelog</a>
    </div>
  `;
  sheet.classList.add("open");
  sheet.classList.remove("bounce");
  requestAnimationFrame(() => {
    requestAnimationFrame(() => {
      sheet.classList.add("bounce");
    });
  });
}

function esc(s) {
  return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
}

function buildCategoryFilter(map) {
  Promise.all([
    fetch(`${window.API_BASE || ""}/api/categories/top`).then((r) => r.json()),
    fetch(`${window.API_BASE || ""}/api/categories`).then((r) => r.json()),
  ]).then(([rawTop, allCategories]) => {
    const exclude = new Set(["其他", "中餐"]);
    const inject = ["燒肉", "壽司"];
    let topCategories = rawTop.filter((c) => !exclude.has(c));
    inject.forEach((c) => { if (!topCategories.includes(c)) topCategories.push(c); });
    topCategories = topCategories.slice(0, 10);

    // --- Chip row ---
    const chipRow = document.createElement("div");
    chipRow.className = "category-chips";
    chipRow.addEventListener("click", (e) => e.stopPropagation());

    function updateChips() {
      chipRow.querySelectorAll(".category-chip").forEach((chip) => {
        if (chip === moreChip) {
          const inTop = selectedCategory === "" || topCategories.includes(selectedCategory);
          chip.classList.toggle("active", !inTop);
        } else {
          chip.classList.toggle("active", chip.dataset.cat === selectedCategory);
        }
      });
    }

    function selectCat(cat) {
      selectedCategory = cat;
      updateChips();
      if (currentPosition) {
        fetchAndRenderRestaurants(map, currentPosition[0], currentPosition[1]);
      }
    }

    const allChip = document.createElement("div");
    allChip.className = "category-chip" + (selectedCategory === "" ? " active" : "");
    allChip.textContent = "All";
    allChip.dataset.cat = "";
    allChip.addEventListener("click", () => selectCat(""));
    chipRow.appendChild(allChip);

    topCategories.forEach((cat) => {
      const chip = document.createElement("div");
      chip.className = "category-chip" + (selectedCategory === cat ? " active" : "");
      chip.textContent = cat;
      chip.dataset.cat = cat;
      chip.addEventListener("click", () => selectCat(cat));
      chipRow.appendChild(chip);
    });

    const moreChip = document.createElement("div");
    moreChip.className = "category-chip";
    moreChip.textContent = "More ▾";
    moreChip.addEventListener("click", (e) => {
      e.stopPropagation();
      openCategorySheet();
    });
    chipRow.appendChild(moreChip);

    document.getElementById("map").appendChild(chipRow);

    // --- Category bottom sheet ---
    const catSheet = document.createElement("div");
    catSheet.id = "category-sheet";
    document.body.appendChild(catSheet);

    function openCategorySheet() {
      catSheet.innerHTML = `
        <div class="bs-handle"></div>
        <input class="cat-search" type="search" placeholder="Search categories..." autocomplete="off">
        <div class="cat-list"></div>
      `;

      const input = catSheet.querySelector(".cat-search");
      const list = catSheet.querySelector(".cat-list");

      function renderList(query) {
        const filtered = query
          ? allCategories.filter((c) => c.toLowerCase().includes(query.toLowerCase()))
          : allCategories;

        list.innerHTML = [
          `<div class="cat-item${selectedCategory === "" ? " active" : ""}" data-cat="">All</div>`,
          ...filtered.map((c) => `<div class="cat-item${selectedCategory === c ? " active" : ""}" data-cat="${esc(c)}">${esc(c)}</div>`),
        ].join("");

        list.querySelectorAll(".cat-item").forEach((item) => {
          item.addEventListener("click", () => {
            selectCat(item.dataset.cat);
            closeCategorySheet();
          });
        });
      }

      input.addEventListener("input", () => renderList(input.value));
      renderList("");
      catSheet.classList.add("open");
      setTimeout(() => input.focus(), 320);
    }

    function closeCategorySheet() {
      catSheet.classList.remove("open");
    }

    catSheet.addEventListener("click", (e) => e.stopPropagation());
    map.on("click", closeCategorySheet);

  }).catch((err) => console.error("failed to build category filter:", err));
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

  let lastFetchPosition = null;

  function metersApart([lng1, lat1], [lng2, lat2]) {
    const R = 6371000;
    const dLat = (lat2 - lat1) * Math.PI / 180;
    const dLng = (lng2 - lng1) * Math.PI / 180;
    const a = Math.sin(dLat / 2) ** 2 +
      Math.cos(lat1 * Math.PI / 180) * Math.cos(lat2 * Math.PI / 180) * Math.sin(dLng / 2) ** 2;
    return R * 2 * Math.atan2(Math.sqrt(a), Math.sqrt(1 - a));
  }

  map.addControl(geolocate);
  geolocate.on("geolocate", (e) => {
    const lng = e.coords.longitude;
    const lat = e.coords.latitude;
    const position = [lng, lat];

    currentPosition = position;
    userMarker.setLngLat(position).addTo(map);

    if (!lastFetchPosition || metersApart(lastFetchPosition, position) > 100) {
      lastFetchPosition = position;
      fetchAndRenderRestaurants(map, lng, lat);
    }
  });
}
