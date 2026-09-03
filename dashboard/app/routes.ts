import {
  index,
  layout,
  route,
  type RouteConfig,
} from "@react-router/dev/routes";

export default [
  layout("./routes/app-layout.tsx", [
    index("./routes/overview.tsx"),
    route("live", "./routes/live.tsx"),
    route("sessions", "./routes/sessions.tsx"),
    route("sessions/:sessionId", "./routes/session-detail.tsx"),
    route("runs", "./routes/runs.tsx"),
    route("runs/:runId", "./routes/run-detail.tsx"),
    route("metrics", "./routes/metrics.tsx"),
    route("infrastructure", "./routes/infrastructure.tsx"),
    route("incidents", "./routes/incidents.tsx"),
    route("catalog", "./routes/catalog.tsx"),
  ]),
] satisfies RouteConfig;
