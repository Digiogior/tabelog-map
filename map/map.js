mapboxgl.accessToken = window.MAPBOX_TOKEN;
const options = {
  enableHighAccuracy: true,
  maximumAge: 0,
  timeout: 20000,
};

const DEFAULT_MAP_CENTER = [138.2529, 36.2048];

const userMarker = new mapboxgl.Marker({ color: "#007cbf" });
let restaurantSearchPosition = null;
let pendingRestaurantPosition = null;
let selectedCategory = "";
let selectedMarker = null;
let top5Restaurants = [];
let allRestaurants = [];
let minRating = 0;
let kidsOnly = false;
let currentMap = null;
let currentMapReady = false;
let bannerHideTimerId = null;
let latestAccuracyPosition = null;
let locationAcquisitionGeneration = 0;
let areaPickerReturnState = "welcome";
let areaSearchGeneration = 0;
let locationFlowStarted = false;

function hideBanner(banner) {
  if (!banner) return;
  if (bannerHideTimerId) clearTimeout(bannerHideTimerId);
  banner.style.pointerEvents = "none";
  banner.classList.add("hidden");
  bannerHideTimerId = setTimeout(() => {
    banner.style.display = "none";
    bannerHideTimerId = null;
  }, 350);
}

function showLocationBanner(message, detail = "") {
  const banner = document.getElementById("locating-banner");
  if (!banner) return null;

  if (bannerHideTimerId) {
    clearTimeout(bannerHideTimerId);
    bannerHideTimerId = null;
  }
  banner.style.display = "";
  banner.style.pointerEvents = "none";
  banner.classList.remove("hidden");
  banner.innerHTML = `${message}${detail ? `<br><small>${detail}</small>` : ""}`;
  return banner;
}

function makeLocationAction(label, style, onClick) {
  const button = document.createElement("button");
  button.type = "button";
  button.className = `location-button${style ? ` ${style}` : ""}`;
  button.textContent = label;
  button.addEventListener("click", onClick);
  return button;
}

function showLocationDialog({
  icon = "📍",
  title,
  copy,
  actions,
  privacy = "",
}) {
  const dialog = document.getElementById("location-dialog");
  const extra = document.getElementById("location-extra");
  const actionContainer = document.getElementById("location-actions");
  const privacyElement = document.getElementById("location-privacy");
  if (!dialog || !extra || !actionContainer || !privacyElement) return;

  document.getElementById("location-icon").textContent = icon;
  document.getElementById("location-title").textContent = title;
  document.getElementById("location-copy").textContent = copy;
  extra.replaceChildren();
  actionContainer.replaceChildren();
  actions.forEach(({ label, style = "", onClick }) => {
    actionContainer.appendChild(makeLocationAction(label, style, onClick));
  });
  privacyElement.textContent = privacy;
  privacyElement.hidden = !privacy;
  dialog.hidden = false;
}

function hideLocationDialog() {
  const dialog = document.getElementById("location-dialog");
  if (dialog) dialog.hidden = true;
}

function showWelcomeDialog() {
  showLocationDialog({
    title: "Find restaurants near you",
    copy: "Use your location to show nearby restaurants, or choose an area without sharing your location.",
    actions: [
      { label: "Use my location", style: "primary", onClick: locateUser },
      { label: "Choose an area", onClick: () => openAreaPicker("welcome") },
    ],
    privacy: "Your location is used only to load nearby restaurants.",
  });
}

function showLocationBlockedDialog() {
  hideBanner(document.getElementById("locating-banner"));
  showLocationDialog({
    icon: "🔒",
    title: "Location is blocked",
    copy: "Choose an area now, or allow location in your browser settings and try again.",
    actions: [
      { label: "Choose an area", style: "primary", onClick: () => openAreaPicker("blocked") },
      { label: "How to enable location", onClick: showPermissionHelp },
      { label: "I've enabled it — try again", style: "tertiary", onClick: locateUser },
    ],
  });
}

function detectClientDevice() {
  const ua = navigator.userAgent || "";
  const uaPlatform = navigator.userAgentData && navigator.userAgentData.platform
    ? navigator.userAgentData.platform
    : "";
  const platform = navigator.platform || "";
  const isIPadOS = platform === "MacIntel" && navigator.maxTouchPoints > 1;
  const isIOS = /iPad|iPhone|iPod/i.test(ua) || isIPadOS;
  const isAndroid = /Android/i.test(`${ua} ${uaPlatform}`);

  let browser = "browser";
  if (/EdgiOS|EdgA|Edg\//i.test(ua)) browser = "Edge";
  else if (/SamsungBrowser\//i.test(ua)) browser = "Samsung Internet";
  else if (/CriOS|Chrome\//i.test(ua)) browser = "Chrome";
  else if (/FxiOS|Firefox\//i.test(ua)) browser = "Firefox";
  else if (isIOS && /Safari\//i.test(ua)) browser = "Safari";

  return {
    isIOS,
    isAndroid,
    isIPadOS,
    browser,
  };
}

function permissionGuidanceForDevice() {
  const device = detectClientDevice();
  const appleDevice = device.isIPadOS || /iPad/i.test(navigator.userAgent || "")
    ? "iPad"
    : "iPhone";

  if (device.isIOS && device.browser === "Safari") {
    return {
      title: `Enable location on ${appleDevice}`,
      copy: "Safari needs permission for this website and may also need permission in iOS Settings.",
      steps: [
        "Tap the page menu beside Safari's address bar, then open Website Settings.",
        "Tap Location and choose Allow.",
        `If Location is still blocked, open ${appleDevice} Settings → Apps → Safari → Location and choose Ask or Allow.`,
        "Return to Safari, reload tabelogmap.com, and tap ‘I've enabled it — try again.’",
      ],
    };
  }
  if (device.isIOS) {
    return {
      title: `Enable location on ${appleDevice}`,
      copy: `${device.browser} currently cannot access your ${appleDevice}'s location.`,
      steps: [
        `Open ${appleDevice} Settings → Apps → ${device.browser} → Location. You can also use Privacy & Security → Location Services → ${device.browser}.`,
        "Choose While Using the App.",
        "Turn on Precise Location for the most accurate nearby restaurant results.",
        `Return to ${device.browser}, reload tabelogmap.com, and tap ‘I've enabled it — try again.’`,
        "If the browser asks whether this website may use your location, choose Allow.",
      ],
    };
  }
  if (device.isAndroid) {
    return {
      title: "Enable location on Android",
      copy: `${device.browser} needs permission for this website and may also need permission in Android Settings.`,
      steps: [
        "Tap the site information icon beside the address bar, then Permissions.",
        "Open Location and choose Allow.",
        `If it is still blocked, open Android Settings → Apps → ${device.browser} → Permissions → Location and allow it while using the app.`,
        `Return to ${device.browser}, reload tabelogmap.com, and tap ‘I've enabled it — try again.’`,
      ],
    };
  }
  return {
    title: "Enable location in your browser",
    copy: "Allow location for this website, then try again.",
    steps: [
      "Open this site's settings from the icon beside the address bar.",
      "Change Location to Allow.",
      "Return here, reload the page, and click ‘I've enabled it — try again.’",
    ],
  };
}

function showPermissionHelp() {
  const guidance = permissionGuidanceForDevice();
  showLocationDialog({
    icon: "⚙️",
    title: guidance.title,
    copy: guidance.copy,
    actions: [
      { label: "Choose an area instead", style: "primary", onClick: () => openAreaPicker("blocked") },
      { label: "Back", onClick: showLocationBlockedDialog },
    ],
  });

  const list = document.createElement("ol");
  list.className = "permission-steps";
  guidance.steps.forEach((step) => {
    const item = document.createElement("li");
    item.textContent = step;
    list.appendChild(item);
  });
  document.getElementById("location-extra").appendChild(list);
}

function openAreaPicker(returnState = currentMap ? "map" : "welcome") {
  locationFlowStarted = true;
  areaPickerReturnState = returnState;
  hideLocationDialog();
  const picker = document.getElementById("area-picker");
  picker.hidden = false;
  requestAnimationFrame(() => document.getElementById("area-search-input").focus());
}

function closeAreaPicker() {
  const picker = document.getElementById("area-picker");
  picker.hidden = true;
  if (areaPickerReturnState === "blocked") showLocationBlockedDialog();
  if (areaPickerReturnState === "welcome") showWelcomeDialog();
}

function setSelectedAreaLabel(label) {
  const button = document.getElementById("change-area-btn");
  if (!button) return;
  const conciseLabel = label && label.length <= 22 ? label : "Change area";
  button.textContent = `⌕ ${conciseLabel}`;
  button.setAttribute("aria-label", `Choose another area. Current search: ${label || "map area"}`);
}

function showSearchAreaButton() {
  const button = document.getElementById("search-area-btn");
  if (button) button.hidden = false;
}

function hideSearchAreaButton() {
  const button = document.getElementById("search-area-btn");
  if (button) button.hidden = true;
}

function chooseArea(center, label, { moveMap = true } = {}) {
  stopLocationAcquisition();
  hideBanner(document.getElementById("locating-banner"));
  hideLocationDialog();
  document.getElementById("area-picker").hidden = true;
  latestAccuracyPosition = null;
  userMarker.remove();

  if (!currentMap) {
    setMap(center, 15);
  } else if (moveMap) {
    currentMap.flyTo({ center, zoom: Math.max(currentMap.getZoom(), 15), duration: 600 });
  }

  hideSearchAreaButton();
  setSelectedAreaLabel(label);
  requestRestaurantsForPosition(center);
  showLocationBanner(`Restaurants near ${label}`);
  setTimeout(() => hideBanner(document.getElementById("locating-banner")), 1800);
}

function startMapAreaSelection() {
  hideLocationDialog();
  document.getElementById("area-picker").hidden = true;
  if (!currentMap) setMap(DEFAULT_MAP_CENTER, 5);
  showSearchAreaButton();
  showLocationBanner("Move the map", "Then tap ‘Search this area’");
  setTimeout(() => hideBanner(document.getElementById("locating-banner")), 2200);
}

function areaResultLabel(feature) {
  const properties = feature.properties || {};
  return properties.name_preferred || properties.name || "Selected area";
}

function renderAreaSearchResults(features) {
  const container = document.getElementById("area-search-results");
  const status = document.getElementById("area-search-status");
  const attribution = document.getElementById("area-attribution");
  container.replaceChildren();
  attribution.hidden = features.length === 0;

  if (features.length === 0) {
    status.textContent = "No matching areas found. Try another name or choose on map.";
    return;
  }

  status.textContent = `${features.length} result${features.length === 1 ? "" : "s"}`;
  features.forEach((feature) => {
    const properties = feature.properties || {};
    const coordinates = feature.geometry && feature.geometry.coordinates;
    if (!Array.isArray(coordinates) || coordinates.length < 2) return;

    const button = document.createElement("button");
    button.type = "button";
    button.className = "area-result";

    const name = document.createElement("span");
    name.className = "area-result-name";
    name.textContent = areaResultLabel(feature);
    button.appendChild(name);

    const addressText = properties.full_address || properties.place_formatted || "Japan";
    if (addressText && addressText !== name.textContent) {
      const address = document.createElement("span");
      address.className = "area-result-address";
      address.textContent = addressText;
      button.appendChild(address);
    }

    button.addEventListener("click", () => {
      chooseArea([Number(coordinates[0]), Number(coordinates[1])], areaResultLabel(feature));
    });
    container.appendChild(button);
  });
}

async function searchForArea(query) {
  const status = document.getElementById("area-search-status");
  const results = document.getElementById("area-search-results");
  const attribution = document.getElementById("area-attribution");
  const generation = ++areaSearchGeneration;
  status.textContent = "Searching…";
  results.replaceChildren();
  attribution.hidden = true;

  try {
    const url = new URL("https://api.mapbox.com/search/searchbox/v1/forward");
    url.searchParams.set("q", query);
    url.searchParams.set("access_token", window.MAPBOX_TOKEN);
    url.searchParams.set("country", "JP");
    url.searchParams.set("language", navigator.language || "en");
    url.searchParams.set("limit", "6");
    url.searchParams.set("types", "poi,address,street,neighborhood,locality,city,place,district");
    const response = await fetch(url);
    if (!response.ok) throw new Error(`Area search returned ${response.status}`);
    const data = await response.json();
    if (generation !== areaSearchGeneration) return;
    renderAreaSearchResults(Array.isArray(data.features) ? data.features : []);
  } catch (error) {
    if (generation !== areaSearchGeneration) return;
    console.error("failed to search for area:", error);
    status.textContent = "Area search is unavailable. Pick a popular area or choose on map.";
  }
}

function setLocateButtonBusy(isBusy) {
  const locateBtn = document.getElementById("locate-btn");
  if (!locateBtn) return;
  locateBtn.disabled = isBusy;
  locateBtn.setAttribute("aria-busy", String(isBusy));
  locateBtn.style.opacity = isBusy ? "0.6" : "";
}

function stopLocationAcquisition() {
  locationAcquisitionGeneration += 1;
  setLocateButtonBusy(false);
}

function locateUser() {
  locationFlowStarted = true;
  stopLocationAcquisition();
  const acquisitionGeneration = locationAcquisitionGeneration;
  hideLocationDialog();
  document.getElementById("area-picker").hidden = true;
  showLocationBanner("Locating…", "Waiting for your phone's location");
  setLocateButtonBusy(true);

  if (!navigator.geolocation) {
    errorLocation(
      "Location is unavailable",
      "This browser cannot provide a location. Choose an area to continue.",
    );
    return;
  }

  navigator.geolocation.getCurrentPosition(
    (position) => {
      if (acquisitionGeneration !== locationAcquisitionGeneration) return;
      const accuracy = position.coords.accuracy;
      const latitude = position.coords.latitude;
      const longitude = position.coords.longitude;
      if (![accuracy, latitude, longitude].every(Number.isFinite)) {
        console.warn("Ignoring invalid geolocation reading");
        return;
      }

      finishLocation(position);
    },
    (error) => {
      if (acquisitionGeneration !== locationAcquisitionGeneration) return;
      if (error.code === 1) {
        stopLocationAcquisition();
        showLocationBlockedDialog();
        return;
      }

      errorLocation(
        error.code === 3 ? "Location timed out" : "Location is unavailable",
        error.code === 3
          ? "Your phone took too long to respond. Try again or choose an area."
          : "Your phone could not provide a location. Try again or choose an area.",
      );
    },
    options,
  );
}

function restaurantToProps(r) {
  return {
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
  };
}

function clearTop5Highlight() {
  document.querySelectorAll(".top5-card.active").forEach((c) => c.classList.remove("active"));
}

function highlightTop5Card(url) {
  clearTop5Highlight();
  const card = document.querySelector(`.top5-card[data-url="${CSS.escape(url)}"]`);
  if (card) {
    card.classList.add("active");
    card.scrollIntoView({ behavior: "smooth", block: "nearest", inline: "center" });
  }
}

function renderTop5Strip(map) {
  let strip = document.getElementById("top5-strip");
  if (!strip) {
    strip = document.createElement("div");
    strip.id = "top5-strip";
    document.body.appendChild(strip);
  }

  if (!selectedCategory || top5Restaurants.length === 0) {
    strip.classList.add("hidden");
    return;
  }

  strip.classList.remove("hidden");

  const priceShort = (min, max) => {
    if (!min && !max) return null;
    return `¥${(min || max).toLocaleString()}〜`;
  };

  strip.innerHTML = `
    <div class="top5-header">
      <div class="top5-label">Top ${top5Restaurants.length} in ${esc(selectedCategory)}</div>
      <button class="top5-close" aria-label="Close">✕</button>
    </div>
    <div class="top5-cards">
      ${top5Restaurants.map((r, i) => {
        const photos = r.Photos || [];
        const imgHtml = photos[0]
          ? `<img class="top5-card-photo" src="${esc(photos[0])}" alt="" loading="lazy">`
          : `<div class="top5-card-photo top5-card-photo-empty"></div>`;
        const price = priceShort(r.DinnerMinPrice, r.DinnerMaxPrice)
          || priceShort(r.LunchMinPrice, r.LunchMaxPrice)
          || "";
        const ratingText = r.Rating > 0 ? `⭐ ${r.Rating}` : "No rating";
        return `
          <div class="top5-card" data-url="${esc(r.URL)}" data-idx="${i}">
            <div class="top5-card-img-wrap">
              ${imgHtml}
              <div class="top5-card-rank">${i + 1}</div>
            </div>
            <div class="top5-card-body">
              <div class="top5-card-name">${esc(r.Name)}</div>
              <div class="top5-card-rating">${ratingText}</div>
              ${price ? `<div class="top5-card-price">${price}</div>` : ""}
            </div>
          </div>`;
      }).join("")}
    </div>
  `;

  strip.querySelector(".top5-close").addEventListener("click", (e) => {
    e.stopPropagation();
    strip.classList.add("hidden");
  });

  strip.querySelectorAll(".top5-card").forEach((card) => {
    card.addEventListener("click", () => {
      const r = top5Restaurants[+card.dataset.idx];
      deselectRestaurant();

      const el = document.createElement("div");
      el.className = "selected-marker";
      el.addEventListener("click", (e) => e.stopPropagation());
      selectedMarker = new mapboxgl.Marker({ element: el, anchor: "center" })
        .setLngLat([r.Longitude, r.Latitude])
        .addTo(map);

      map.flyTo({ center: [r.Longitude, r.Latitude], zoom: Math.max(map.getZoom(), 17), duration: 600 });
      showBottomSheet(restaurantToProps(r));
      highlightSelectedMarker(r.URL);
    });
  });
}

function highlightSelectedMarker(url) {
  if (!currentMap || !currentMap.getLayer("restaurants")) return;
  currentMap.setPaintProperty("restaurants", "circle-color", [
    "case", ["==", ["get", "url"], url], "#e74c3c", "#c0c0c0",
  ]);
  currentMap.setPaintProperty("restaurants", "circle-stroke-color", [
    "case", ["==", ["get", "url"], url], "#fff", "#e0e0e0",
  ]);
  if (currentMap.getLayer("clusters")) {
    currentMap.setPaintProperty("clusters", "circle-color", "#c0c0c0");
    currentMap.setPaintProperty("clusters", "circle-stroke-color", "#e0e0e0");
  }
}

function resetMarkerColors() {
  if (!currentMap || !currentMap.getLayer("restaurants")) return;
  currentMap.setPaintProperty("restaurants", "circle-color", "#e74c3c");
  currentMap.setPaintProperty("restaurants", "circle-stroke-color", "#fff");
  if (currentMap.getLayer("clusters")) {
    currentMap.setPaintProperty("clusters", "circle-color", [
      "step", ["get", "point_count"], "#f28cb1", 10, "#e74c3c", 30, "#c0392b",
    ]);
    currentMap.setPaintProperty("clusters", "circle-stroke-color", "#fff");
  }
}

function deselectRestaurant() {
  const sheet = document.getElementById("bottom-sheet");
  if (sheet) {
    sheet.classList.remove("open", "bounce");
    sheet.style.animation = "";
    sheet.style.transform = "";
    sheet.style.transition = "";
  }
  const locateBtn = document.getElementById("locate-btn");
  if (locateBtn) locateBtn.style.bottom = "24px";
  if (selectedMarker) { selectedMarker.remove(); selectedMarker = null; }
  clearTop5Highlight();
  resetMarkerColors();
}

function zoomForAccuracy(accuracy) {
  if (accuracy <= 50) return 18;
  if (accuracy <= 100) return 17;
  if (accuracy <= 200) return 16;
  if (accuracy <= 500) return 15;
  if (accuracy <= 1000) return 14;
  if (accuracy <= 2500) return 13;
  return 11;
}

function displayPosition(position) {
  const center = [
    position.coords.longitude,
    position.coords.latitude,
  ];
  latestAccuracyPosition = {
    center,
    accuracy: position.coords.accuracy,
  };

  const hadMap = Boolean(currentMap);
  if (!currentMap) setMap(center);

  userMarker.setLngLat(center).addTo(currentMap);
  if (hadMap) {
    currentMap.easeTo({
      center,
      zoom: Math.max(
        currentMap.getZoom(),
        zoomForAccuracy(position.coords.accuracy),
      ),
      duration: 600,
    });
  }
}

function requestRestaurantsForPosition(center) {
  restaurantSearchPosition = center;
  pendingRestaurantPosition = center;

  if (!currentMap) setMap(center);
  if (!currentMapReady) return;

  pendingRestaurantPosition = null;
  fetchAndRenderRestaurants(currentMap, center[0], center[1]);
}

function loadRestaurantsForPosition(position) {
  requestRestaurantsForPosition([
    position.coords.longitude,
    position.coords.latitude,
  ]);
}

function finishLocation(position) {
  displayPosition(position);
  stopLocationAcquisition();
  hideBanner(document.getElementById("locating-banner"));
  hideLocationDialog();
  setSelectedAreaLabel("My location");
  loadRestaurantsForPosition(position);
}

function errorLocation(
  message = "Location is unavailable",
  detail = "Try again or choose an area to continue.",
) {
  stopLocationAcquisition();
  hideBanner(document.getElementById("locating-banner"));
  showLocationDialog({
    icon: "⚠️",
    title: message,
    copy: detail,
    actions: [
      { label: "Choose an area", style: "primary", onClick: () => openAreaPicker("welcome") },
      { label: "Try location again", onClick: locateUser },
    ],
  });
}

function passesFilters(r) {
  if (minRating > 0 && !(r.Rating > minRating)) return false;
  if (kidsOnly && !r.GoodForKids) return false;
  return true;
}

function applyFiltersAndRender(map) {
  const filtered = allRestaurants.filter(passesFilters);

  top5Restaurants = filtered
    .filter((r) => r.Rating > 0)
    .sort((a, b) => b.Rating - a.Rating)
    .slice(0, 10);
  renderTop5Strip(map);

  const geojson = {
    type: "FeatureCollection",
    features: filtered.map((r) => ({
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

  const source = map.getSource("restaurants");
  if (source) source.setData(geojson);
  return geojson;
}

function fetchAndRenderRestaurants(map, lng, lat) {
  const url = new URL("/api/restaurants", window.API_BASE || location.origin);
  url.searchParams.set("lat", lat);
  url.searchParams.set("lng", lng);
  if (selectedCategory) url.searchParams.set("category", selectedCategory);

  fetch(url)
    .then((res) => res.json())
    .then((restaurants) => {
      allRestaurants = restaurants;
      const geojson = applyFiltersAndRender(map);

      if (!map.getSource("restaurants")) {
        map.addSource("restaurants", {
          type: "geojson",
          data: geojson,
          cluster: true,
          clusterMaxZoom: 15,
          clusterRadius: 50,
        });

        const isMobile = window.innerWidth <= 650;

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
            "circle-radius": isMobile ? [
              "step", ["get", "point_count"],
              14, 10,
              20, 30,
              26
            ] : [
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
            "circle-radius": isMobile ? 7 : 16,
            "circle-color": "#e74c3c",
            "circle-stroke-width": isMobile ? 2 : 2.5,
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
          highlightSelectedMarker(p.url);
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

function buildGoogleMapsUrl(p) {
  const name = typeof p.name === "string" ? p.name.trim() : "";
  const address = typeof p.address === "string" ? p.address.trim() : "";
  const latitude = Number(p.lat);
  const longitude = Number(p.lng);
  const hasCoordinates = Number.isFinite(latitude)
    && Number.isFinite(longitude)
    && latitude >= -90
    && latitude <= 90
    && longitude >= -180
    && longitude <= 180;

  // A name/address search can leave Google Maps centered on a location without
  // displaying a marker when no matching place exists. A coordinate search
  // always places a pin at the restaurant's exact scraped location.
  const query = hasCoordinates
    ? `${latitude},${longitude}`
    : [name, address].filter(Boolean).join(", ");
  if (!query) return "";

  return `https://www.google.com/maps/search/?api=1&query=${encodeURIComponent(query)}`;
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
  const googleMapsUrl = buildGoogleMapsUrl(p);
  const photos = JSON.parse(p.photos || "[]");
  const photoHtml = photos.length > 0 ? `
    <div class="bs-photos">
      <img class="bs-photo-main" src="${photos[0]}" alt="" loading="lazy">
      ${photos.length > 1 ? `<div class="bs-photos-row">
        ${photos.slice(1).map((u) => `<img class="bs-photo-thumb" src="${u}" alt="" loading="lazy">`).join("")}
      </div>` : ""}
    </div>` : "";
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
      <a href="${p.url}" target="_blank" rel="noopener noreferrer" class="bs-action-btn">🍽 View on Tabelog</a>
      ${googleMapsUrl ? `<a href="${googleMapsUrl}" target="_blank" rel="noopener noreferrer" class="bs-action-btn">📍 Google Maps</a>` : ""}
    </div>
  `;
  sheet.classList.add("open");
  sheet.classList.remove("bounce");
  requestAnimationFrame(() => {
    requestAnimationFrame(() => {
      sheet.classList.add("bounce");
      const locateBtn = document.getElementById("locate-btn");
      if (locateBtn) locateBtn.style.bottom = `${sheet.getBoundingClientRect().height + 12}px`;
    });
  });
  highlightTop5Card(p.url);
}

function esc(s) {
  return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
}

function buildFilterChips(map) {
  const row = document.createElement("div");
  row.className = "filter-chips";
  row.addEventListener("click", (e) => e.stopPropagation());

  const ratingChips = [];

  function updateActive() {
    ratingChips.forEach((c) => {
      c.classList.toggle("active", Number(c.dataset.min) === minRating);
    });
    kidsChip.classList.toggle("active", kidsOnly);
  }

  function makeRatingChip(threshold) {
    const chip = document.createElement("div");
    chip.className = "category-chip";
    chip.dataset.min = String(threshold);
    chip.innerHTML = `<span class="filter-chip-star">★</span>${threshold}+`;
    chip.addEventListener("click", () => {
      minRating = (minRating === threshold) ? 0 : threshold;
      updateActive();
      deselectRestaurant();
      applyFiltersAndRender(map);
    });
    ratingChips.push(chip);
    row.appendChild(chip);
  }

  makeRatingChip(2);
  makeRatingChip(3);

  const kidsChip = document.createElement("div");
  kidsChip.className = "category-chip";
  kidsChip.textContent = "👶 Good for kids";
  kidsChip.addEventListener("click", () => {
    kidsOnly = !kidsOnly;
    updateActive();
    deselectRestaurant();
    applyFiltersAndRender(map);
  });
  row.appendChild(kidsChip);

  updateActive();
  document.getElementById("map").appendChild(row);
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
      deselectRestaurant();
      if (restaurantSearchPosition) {
        requestRestaurantsForPosition(restaurantSearchPosition);
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

function setMap(center, initialZoom = null) {
  const map = new mapboxgl.Map({
    container: "map",
    center: center,
    zoom: initialZoom !== null
      ? initialZoom
      : latestAccuracyPosition
        ? zoomForAccuracy(latestAccuracyPosition.accuracy)
        : 15,
  });
  currentMap = map;
  currentMapReady = false;

  map.on("load", () => {
    currentMapReady = true;

    if (pendingRestaurantPosition) {
      const position = pendingRestaurantPosition;
      pendingRestaurantPosition = null;
      fetchAndRenderRestaurants(map, position[0], position[1]);
    }
  });
  map.on("dragend", showSearchAreaButton);
  buildCategoryFilter(map);
  buildFilterChips(map);

  const locateBtn = document.createElement("button");
  locateBtn.id = "locate-btn";
  locateBtn.type = "button";
  locateBtn.setAttribute("aria-label", "Use my location");
  locateBtn.title = "Use my location";
  locateBtn.innerHTML = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="#e74c3c" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" width="22" height="22"><circle cx="12" cy="12" r="7"/><line x1="12" y1="1" x2="12" y2="5"/><line x1="12" y1="19" x2="12" y2="23"/><line x1="1" y1="12" x2="5" y2="12"/><line x1="19" y1="12" x2="23" y2="12"/><circle cx="12" cy="12" r="2.5" fill="#e74c3c" stroke="none"/></svg>`;
  locateBtn.addEventListener("click", (e) => {
    e.stopPropagation();
    locateUser();
  });
  document.body.appendChild(locateBtn);
  setLocateButtonBusy(false);

  const changeAreaBtn = document.createElement("button");
  changeAreaBtn.id = "change-area-btn";
  changeAreaBtn.type = "button";
  changeAreaBtn.textContent = "⌕ Change area";
  changeAreaBtn.addEventListener("click", (event) => {
    event.stopPropagation();
    openAreaPicker("map");
  });
  document.body.appendChild(changeAreaBtn);

  const searchAreaBtn = document.createElement("button");
  searchAreaBtn.id = "search-area-btn";
  searchAreaBtn.type = "button";
  searchAreaBtn.textContent = "Search this area";
  searchAreaBtn.hidden = true;
  searchAreaBtn.addEventListener("click", (event) => {
    event.stopPropagation();
    const centerPoint = map.getCenter();
    chooseArea([centerPoint.lng, centerPoint.lat], "this map area", { moveMap: false });
  });
  document.body.appendChild(searchAreaBtn);
}

function bindLocationFlow() {
  document.getElementById("area-picker-close").addEventListener("click", closeAreaPicker);
  document.getElementById("choose-on-map").addEventListener("click", startMapAreaSelection);
  document.getElementById("area-search-form").addEventListener("submit", (event) => {
    event.preventDefault();
    const query = document.getElementById("area-search-input").value.trim();
    if (query) searchForArea(query);
  });
  document.querySelectorAll(".popular-area").forEach((button) => {
    button.addEventListener("click", () => {
      chooseArea(
        [Number(button.dataset.lng), Number(button.dataset.lat)],
        button.dataset.label,
      );
    });
  });
}

async function beginLocationFlow() {
  showWelcomeDialog();
  if (!navigator.geolocation) {
    errorLocation(
      "Location is unavailable",
      "This browser cannot provide a location. Choose an area to continue.",
    );
    return;
  }

  if (!navigator.permissions || !navigator.permissions.query) return;
  try {
    const permission = await navigator.permissions.query({ name: "geolocation" });
    if (locationFlowStarted) return;
    if (permission.state === "granted") locateUser();
    if (permission.state === "denied") showLocationBlockedDialog();
  } catch (error) {
    // Safari versions without a geolocation Permissions query still show the
    // normal onboarding and request permission after a user gesture.
  }
}

bindLocationFlow();
beginLocationFlow();
