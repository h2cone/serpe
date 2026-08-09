import {
  type RouteConfig,
  index,
  layout,
  route,
} from "@react-router/dev/routes";

export default [
  layout("routes/_layout.tsx", [
    index("routes/_index.tsx"),
    route("sessions/new", "routes/sessions.new.ts"),
    route("sessions/:id", "routes/sessions.$id.tsx"),
  ]),
] satisfies RouteConfig;
