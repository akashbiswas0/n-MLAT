"use client";

import "leaflet/dist/leaflet.css";

import { useEffect, useMemo, useRef, useState } from "react";
import type { LayerGroup, Map as LeafletMap, Polyline, Circle, Marker, CircleMarker } from "leaflet";
import L from "leaflet";

type SensorContribution = {
  sensor_id: number;
  sensor_name: string;
  lat: number;
  lon: number;
  alt: number;
  residual_m: number;
  clock_adjustment_ns: number;
  clock_jitter_ns: number;
  clock_samples: number;
  clock_health: string;
};

type BroadcastMessage = {
  icao: string;
  lat: number;
  lon: number;
  alt: number;
  cost: number;
  num_sensors: number;
  ground_speed_mps: number;
  track_samples: number;
  rms_residual_m: number;
  uncertainty_m: number;
  gdop: number;
  quality_score: number;
  quality_label: string;
  contributors: SensorContribution[];
  fixes?: number;
  last_seen_unix?: number;
};

type WaitingAircraft = {
  icao: string;
  sensors: number;
  frames: number;
  last_seen_utc: string;
  last_seen_unix: number;
  approx_lat: number;
  approx_lon: number;
};

type SellerSummary = {
  peer_id: string;
  name: string;
  connected_since: string;
};

type AnalyticsSummary = {
  total_fixes: number;
  fix_rate_per_min: number;
  avg_sensors: number;
  avg_uncertainty_m: number;
  avg_quality_score: number;
  avg_gdop: number;
  quality_label: string;
  tracked_aircraft: number;
};

type LiveState = {
  connected_sellers: number;
  raw_aircraft: number;
  active: (BroadcastMessage & { fixes: number; last_seen_unix: number })[];
  sellers: SellerSummary[];
  analytics: AnalyticsSummary;
  waiting: WaitingAircraft[];
};

type WSMessage =
  | { type: "fix"; data: BroadcastMessage }
  | { type: "state"; data: LiveState };

type AircraftView = BroadcastMessage & {
  fixes: number;
  lastSeen: number;
  history: [number, number][];
  stale: boolean;
};

const AIRCRAFT_RETENTION_MS = 10 * 60 * 1000;
const STALE_AFTER_MS = 2 * 60 * 1000;
const MAX_HISTORY_POINTS = 32;
const MAX_COST = 2e-3;
const MAX_GROUND_SPEED_MPS = 750;
const MAX_UNCERTAINTY_M = 18000;

function makeIcon() {
  return L.divIcon({
    className: "aircraft-icon",
    html: `<div class="ac-marker"><div class="ac-ping"></div><div class="ac-dot"></div></div>`,
    iconSize: [28, 28],
    iconAnchor: [14, 14],
  });
}

function haversineKm(lat1: number, lon1: number, lat2: number, lon2: number) {
  const toRad = (value: number) => (value * Math.PI) / 180;
  const R = 6371;
  const dLat = toRad(lat2 - lat1);
  const dLon = toRad(lon2 - lon1);
  const a =
    Math.sin(dLat / 2) ** 2 +
    Math.cos(toRad(lat1)) * Math.cos(toRad(lat2)) * Math.sin(dLon / 2) ** 2;
  return 2 * R * Math.asin(Math.sqrt(a));
}

function qualityClass(label?: string) {
  switch ((label ?? "").toUpperCase()) {
    case "HIGH":
      return "quality-high";
    case "MED":
      return "quality-med";
    default:
      return "quality-low";
  }
}

function healthClass(label?: string) {
  switch ((label ?? "").toLowerCase()) {
    case "stable":
      return "health-stable";
    case "watch":
      return "health-watch";
    case "unstable":
      return "health-unstable";
    default:
      return "health-learning";
  }
}

function shouldDisplayFix(data: BroadcastMessage, existing?: AircraftView) {
  if (![data.lat, data.lon, data.cost].every(Number.isFinite)) return false;
  if (data.num_sensors < 4) return false;
  if (data.cost > MAX_COST) return false;
  if ((data.ground_speed_mps ?? 0) > MAX_GROUND_SPEED_MPS) return false;
  if (data.uncertainty_m && data.uncertainty_m > MAX_UNCERTAINTY_M) return false;
  if (data.quality_score && data.quality_score < 20) return false;
  if (existing) {
    const jumpKm = haversineKm(existing.lat, existing.lon, data.lat, data.lon);
    if (existing.fixes > 3 && jumpKm > 180 && (data.ground_speed_mps ?? 0) > 500 && data.cost > 1e-4) {
      return false;
    }
  }
  return true;
}

function buildWsUrl() {
  if (process.env.NEXT_PUBLIC_WS_URL) return process.env.NEXT_PUBLIC_WS_URL;
  if (typeof window === "undefined") return "";
  const proto = window.location.protocol === "https:" ? "wss" : "ws";
  return `${proto}://${window.location.host}/ws`;
}

export function MLATDashboard() {
  const mapRef = useRef<LeafletMap | null>(null);
  const mapNodeRef = useRef<HTMLDivElement | null>(null);
  const geometryLinesRef = useRef<LayerGroup | null>(null);
  const geometrySensorsRef = useRef<LayerGroup | null>(null);
  const uncertaintyCircleRef = useRef<Circle | null>(null);
  const markerRef = useRef<Record<string, Marker>>({});
  const trailRef = useRef<Record<string, Polyline>>({});
  const sensorMarkerRef = useRef<CircleMarker[]>([]);

  const [aircraft, setAircraft] = useState<Record<string, AircraftView>>({});
  const [waitingAircraft, setWaitingAircraft] = useState<WaitingAircraft[]>([]);
  const [analytics, setAnalytics] = useState<AnalyticsSummary>({
    total_fixes: 0,
    fix_rate_per_min: 0,
    avg_sensors: 0,
    avg_uncertainty_m: 0,
    avg_quality_score: 0,
    avg_gdop: 0,
    quality_label: "—",
    tracked_aircraft: 0,
  });
  const [sellers, setSellers] = useState<SellerSummary[]>([]);
  const [connectedSellers, setConnectedSellers] = useState(0);
  const [selectedICAO, setSelectedICAO] = useState<string | null>(null);
  const [wsStatus, setWsStatus] = useState<"OFFLINE" | "LIVE" | "RECONNECTING">("OFFLINE");
  const [clock, setClock] = useState("--:--:-- UTC");

  useEffect(() => {
    setClock(new Date().toUTCString().slice(17, 25) + " UTC");
    const id = window.setInterval(() => {
      const now = new Date();
      const h = now.getUTCHours().toString().padStart(2, "0");
      const m = now.getUTCMinutes().toString().padStart(2, "0");
      const s = now.getUTCSeconds().toString().padStart(2, "0");
      setClock(`${h}:${m}:${s} UTC`);
    }, 1000);
    return () => window.clearInterval(id);
  }, []);

  useEffect(() => {
    if (!mapNodeRef.current || mapRef.current) return;

    const map = L.map(mapNodeRef.current, { zoomControl: true, attributionControl: false }).setView([51.5, 0], 6);
    L.tileLayer("https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png", { maxZoom: 18 }).addTo(map);
    mapRef.current = map;
    geometryLinesRef.current = L.layerGroup().addTo(map);
    geometrySensorsRef.current = L.layerGroup().addTo(map);
  }, []);

  useEffect(() => {
    const id = window.setInterval(() => {
      setAircraft((current) => {
        const now = Date.now();
        const next = { ...current };
        let changed = false;
        Object.entries(next).forEach(([icao, value]) => {
          if (now - value.lastSeen > AIRCRAFT_RETENTION_MS) {
            markerRef.current[icao]?.remove();
            trailRef.current[icao]?.remove();
            delete markerRef.current[icao];
            delete trailRef.current[icao];
            delete next[icao];
            changed = true;
          } else {
            value.stale = now - value.lastSeen > STALE_AFTER_MS;
          }
        });
        return changed ? next : { ...next };
      });
    }, 10000);

    return () => window.clearInterval(id);
  }, []);

  useEffect(() => {
    const ws = new WebSocket(buildWsUrl());
    let reconnectTimer: number | null = null;

    ws.onopen = () => setWsStatus("LIVE");

    ws.onmessage = (event) => {
      try {
        const message = JSON.parse(event.data) as WSMessage | BroadcastMessage;
        if ("type" in message && message.type === "fix") {
          setAircraft((current) => {
            const next = { ...current };
            const existing = next[message.data.icao];
            if (!shouldDisplayFix(message.data, existing)) return current;
            next[message.data.icao] = {
              ...(existing ?? {
                fixes: 0,
                history: [],
                lastSeen: Date.now(),
                stale: false,
              }),
              ...message.data,
              fixes: (existing?.fixes ?? 0) + 1,
              history: [...(existing?.history ?? []), [message.data.lat, message.data.lon]].slice(-MAX_HISTORY_POINTS),
              lastSeen: Date.now(),
              stale: false,
            };
            return next;
          });
          return;
        }
        if ("type" in message && message.type === "state") {
          setConnectedSellers(message.data.connected_sellers || 0);
          setWaitingAircraft(message.data.waiting || []);
          setAnalytics(message.data.analytics || analytics);
          setSellers(message.data.sellers || []);
          setAircraft((current) => {
            const next = { ...current };
            for (const item of message.data.active || []) {
              const existing = next[item.icao];
              if (!shouldDisplayFix(item, existing)) continue;
              next[item.icao] = {
                ...(existing ?? {
                  fixes: 0,
                  history: [],
                  lastSeen: Date.now(),
                  stale: false,
                }),
                ...item,
                fixes: item.fixes ?? existing?.fixes ?? 0,
                history: [...(existing?.history ?? []), [item.lat, item.lon]].slice(-MAX_HISTORY_POINTS),
                lastSeen: item.last_seen_unix ? item.last_seen_unix * 1000 : Date.now(),
                stale: false,
              };
            }
            return next;
          });
          setSelectedICAO((current) => current ?? message.data.active?.[0]?.icao ?? null);
          return;
        }
        if ("icao" in message) {
          const fix = message as BroadcastMessage;
          setAircraft((current) => {
            const next = { ...current };
            const existing = next[fix.icao];
            if (!shouldDisplayFix(fix, existing)) return current;
            next[fix.icao] = {
              ...(existing ?? { fixes: 0, history: [], lastSeen: Date.now(), stale: false }),
              ...fix,
              fixes: (existing?.fixes ?? 0) + 1,
              history: [...(existing?.history ?? []), [fix.lat, fix.lon]].slice(-MAX_HISTORY_POINTS),
              lastSeen: Date.now(),
              stale: false,
            };
            return next;
          });
        }
      } catch (error) {
        console.error("WebSocket parse error", error);
      }
    };

    ws.onclose = () => {
      setWsStatus("RECONNECTING");
      reconnectTimer = window.setTimeout(() => window.location.reload(), 3000);
    };

    ws.onerror = () => ws.close();

    return () => {
      if (reconnectTimer) window.clearTimeout(reconnectTimer);
      ws.close();
    };
  }, []);

  useEffect(() => {
    const map = mapRef.current;
    if (!map) return;

    Object.entries(aircraft).forEach(([icao, view]) => {
      let marker = markerRef.current[icao];
      let trail = trailRef.current[icao];

      if (!marker) {
        marker = L.marker([view.lat, view.lon], { icon: makeIcon() })
          .bindTooltip(icao, { permanent: false, direction: "top" })
          .on("click", () => setSelectedICAO(icao))
          .addTo(map);
        markerRef.current[icao] = marker;
      } else {
        marker.setLatLng([view.lat, view.lon]);
      }

      if (!trail) {
        trail = L.polyline(view.history, {
          color: "#00ff88",
          weight: 2,
          opacity: 0.4,
          lineCap: "round",
          lineJoin: "round",
          dashArray: "2 10",
        }).addTo(map);
        trailRef.current[icao] = trail;
      } else {
        trail.setLatLngs(view.history);
      }
    });
  }, [aircraft]);

  const selectedAircraft = selectedICAO ? aircraft[selectedICAO] : null;

  useEffect(() => {
    const map = mapRef.current;
    const geometryLines = geometryLinesRef.current;
    const geometrySensors = geometrySensorsRef.current;
    if (!map || !geometryLines || !geometrySensors) return;

    geometryLines.clearLayers();
    geometrySensors.clearLayers();
    sensorMarkerRef.current = [];
    if (uncertaintyCircleRef.current) {
      uncertaintyCircleRef.current.remove();
      uncertaintyCircleRef.current = null;
    }

    if (!selectedAircraft) return;

    for (const sensor of selectedAircraft.contributors || []) {
      const color =
        sensor.clock_health === "unstable"
          ? "#ff4455"
          : sensor.clock_health === "watch"
            ? "#ffaa00"
            : "#00ff88";
      L.polyline(
        [
          [selectedAircraft.lat, selectedAircraft.lon],
          [sensor.lat, sensor.lon],
        ],
        {
          color,
          weight: 1.5,
          opacity: 0.22,
          dashArray: "4 8",
        },
      ).addTo(geometryLines);
      const marker = L.circleMarker([sensor.lat, sensor.lon], {
        radius: 4,
        color,
        weight: 1,
        fillOpacity: 0.85,
      })
        .bindTooltip(`${sensor.sensor_name || sensor.sensor_id}<br/>Residual ${Math.round(sensor.residual_m || 0)}m`)
        .addTo(geometrySensors);
      sensorMarkerRef.current.push(marker);
    }

    if (selectedAircraft.uncertainty > 0) {
      uncertaintyCircleRef.current = L.circle([selectedAircraft.lat, selectedAircraft.lon], {
        radius: selectedAircraft.uncertainty,
        color: "#00ff88",
        opacity: 0.3,
        fillOpacity: 0.04,
        weight: 1,
      }).addTo(map);
    }
  }, [selectedAircraft]);

  const activeRows = useMemo(() => {
    return Object.values(aircraft)
      .sort((a, b) => (b.track_samples || 0) - (a.track_samples || 0) || a.icao.localeCompare(b.icao))
      .slice(0, 8);
  }, [aircraft]);

  const waitingRows = useMemo(() => waitingAircraft.filter((item) => !aircraft[item.icao]).slice(0, 6), [waitingAircraft, aircraft]);

  return (
    <div className="dashboard-shell">
      <header className="topbar">
        <div className="logo">4D<span>SKY</span> · MLAT</div>
        <div className="status-bar">
          <div>
            <span className={`conn-dot ${wsStatus === "LIVE" ? "live" : ""}`} />
            <span>{wsStatus}</span>
          </div>
          <div>
            AIRCRAFT <span className="val">{analytics.tracked_aircraft || Object.keys(aircraft).length}</span>
          </div>
          <div>
            FIXES <span className="val">{analytics.total_fixes || 0}</span>
          </div>
          <div>
            NETWORK <span className="val">NEURON/P2P</span>
          </div>
        </div>
        <div className="clock">{clock}</div>
      </header>

      <main className="main-grid">
        <section className="map-shell">
          <div ref={mapNodeRef} className="map-canvas" />
          <div className="scanline" />
        </section>

        <aside className="sidebar">
          <section className="panel">
            <div className="panel-title">Tracked Aircraft</div>
            {activeRows.length === 0 ? (
              <div className="empty">Waiting for MLAT tracks from the backend websocket.</div>
            ) : (
              <>
                {activeRows.map((ac) => {
                  const stale = Date.now() - ac.lastSeen > STALE_AFTER_MS;
                  return (
                    <button
                      key={ac.icao}
                      className={`track-card ${selectedICAO === ac.icao ? "selected" : ""}`}
                      onClick={() => setSelectedICAO(ac.icao)}
                    >
                      <div className="track-card-top">
                        <div className="icao">{ac.icao}</div>
                        <div className={`pill ${qualityClass(ac.quality_label)}`}>{ac.quality_label || "LOW"}</div>
                      </div>
                      <div className="badge-row">
                        <span className="badge">Alt {Math.round(ac.alt * 3.28084).toLocaleString()}ft</span>
                        <span className="badge">Sensors {ac.num_sensors}</span>
                        <span className="badge">Track {ac.track_samples || 0}</span>
                        <span className="badge">Unc {Math.round(ac.uncertainty_m || 0)}m</span>
                        <span className={`badge ${stale ? "badge-dim" : ""}`}>{stale ? "Stale" : "Live"}</span>
                      </div>
                      <div className="track-meta">
                        Fixes <span>{ac.fixes}</span> · Speed <span>{Math.round(ac.ground_speed_mps || 0)} m/s</span> · GDOP{" "}
                        <span>{Number.isFinite(ac.gdop) ? ac.gdop.toFixed(2) : "—"}</span>
                      </div>
                    </button>
                  );
                })}
              </>
            )}
          </section>

          <section className="panel">
            <div className="panel-title">Waiting Groups</div>
            {waitingRows.length === 0 ? (
              <div className="empty">No pending groups right now.</div>
            ) : (
              waitingRows.map((item) => (
                <div key={item.icao} className="waiting-card">
                  <div className="track-card-top">
                    <div className="icao">{item.icao}</div>
                    <div className="pill pill-muted">Waiting</div>
                  </div>
                  <div className="badge-row">
                    <span className="badge">Sensors {item.sensors}/4</span>
                    <span className="badge">Frames {item.frames}</span>
                    <span className="badge">Sellers {connectedSellers}</span>
                  </div>
                </div>
              ))
            )}
          </section>

          <section className="panel">
            <div className="panel-title">Selected Track / Geometry</div>
            {!selectedAircraft ? (
              <div className="empty">Select an aircraft to inspect quality, uncertainty, and contributing sensors.</div>
            ) : (
              <>
                <div className="detail-grid">
                  <DetailCard label="Quality" value={selectedAircraft.quality_label || "LOW"} />
                  <DetailCard label="Uncertainty" value={`${Math.round(selectedAircraft.uncertainty_m || 0)} m`} />
                  <DetailCard label="GDOP" value={Number.isFinite(selectedAircraft.gdop) ? selectedAircraft.gdop.toFixed(2) : "—"} />
                  <DetailCard label="Track / Speed" value={`${selectedAircraft.track_samples || 0} · ${Math.round(selectedAircraft.ground_speed_mps || 0)} m/s`} />
                </div>
                <div className="panel-subtitle">Contributing Sensors</div>
                <div className="sensor-list">
                  {(selectedAircraft.contributors || []).map((sensor) => (
                    <div key={`${selectedAircraft.icao}-${sensor.sensor_id}`} className="sensor-row">
                      <div>
                        <div className="sensor-name">{sensor.sensor_name || `S-${sensor.sensor_id}`}</div>
                        <div className="sensor-meta">
                          Residual {Math.round(sensor.residual_m || 0)}m · Clock {Math.round(sensor.clock_adjustment_ns || 0)}ns
                          <br />
                          Jitter {Math.round(sensor.clock_jitter_ns || 0)}ns · Samples {sensor.clock_samples || 0}
                        </div>
                      </div>
                      <div className={`pill ${healthClass(sensor.clock_health)}`}>{sensor.clock_health || "learning"}</div>
                    </div>
                  ))}
                </div>
              </>
            )}
          </section>

          <section className="panel">
            <div className="panel-title">Network / Analytics</div>
            <div className="detail-grid">
              <DetailCard label="Fix Rate" value={`${Math.round(analytics.fix_rate_per_min || 0)} / min`} />
              <DetailCard label="Avg GDOP" value={analytics.avg_gdop ? analytics.avg_gdop.toFixed(2) : "—"} />
              <DetailCard label="Avg Quality" value={analytics.quality_label || "—"} />
              <DetailCard label="Tracked / Waiting" value={`${analytics.tracked_aircraft || 0} / ${waitingAircraft.length}`} />
            </div>
            <div className="panel-subtitle">Connected Sellers</div>
            <div className="seller-list">
              {sellers.length === 0 ? (
                <div className="empty">Waiting for seller telemetry.</div>
              ) : (
                sellers.slice(0, 5).map((seller) => (
                  <div key={seller.peer_id} className="seller-row">
                    <div>
                      <div className="sensor-name">{seller.name || seller.peer_id}</div>
                      <div className="sensor-meta">
                        {seller.peer_id.slice(0, 12)}...
                        <br />
                        Connected {new Date(seller.connected_since).toLocaleTimeString()}
                      </div>
                    </div>
                    <div className="pill quality-high">Live</div>
                  </div>
                ))
              )}
            </div>
          </section>
        </aside>
      </main>
    </div>
  );
}

function DetailCard({ label, value }: { label: string; value: string }) {
  return (
    <div className="detail-card">
      <div className="detail-label">{label}</div>
      <div className="detail-value">{value}</div>
    </div>
  );
}
