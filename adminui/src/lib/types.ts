export type BadgeKind = "ok" | "warn" | "bad";

export interface ProxyRouteSummary {
  kind?: string;
  name?: string;
  target?: string;
  route_mode?: string;
  proxy_source?: string;
  proxy_url?: string;
  proxy_id?: string;
}

export interface ProxyIdentityRecord {
  proxy_id?: string;
  proxy_url?: string;
  proxy_source?: string;
}

export interface ProxyCheckRecord {
  timestamp?: string;
  success?: boolean;
  tcp_duration_ms?: number;
  connect_duration_ms?: number;
  error_phase?: string;
  error_reason?: string;
  http_status_category?: string;
}

export interface ProxyRouteHealthSummary {
  route?: ProxyRouteSummary;
  health_state?: string;
  last_check?: string;
  last_success?: string;
  history?: ProxyCheckRecord[];
}

export interface SystemTrustSummary {
  enabled?: boolean;
  source?: string;
  source_paths?: string[];
  fallback_note?: string;
}

export interface CertificateMetadata {
  cert_id?: string;
  name?: string;
  description?: string;
  subject_cn?: string;
  issuer_cn?: string;
  not_before?: string;
  not_after?: string;
  source?: string;
  parse_status?: string;
}

export interface ExtraBundleSummary {
  path?: string;
  cert_count?: number;
  parse_errors?: number;
  certificates?: CertificateMetadata[];
}

export interface TLSReport {
  system_trust?: SystemTrustSummary;
  extra_bundle?: ExtraBundleSummary;
}

export interface SystemResponse {
  tls?: TLSReport;
  proxy_identity_map?: ProxyIdentityRecord[];
  proxy_health?: ProxyRouteHealthSummary[];
}

export interface StatusResponse {
  version?: string;
  started_at?: string;
  uptime_seconds?: number;
  health_listen_addr?: string;
  control_plane_base_url?: string;
  control_plane_tunnel_id?: string;
  control_plane_max_inflight?: number;
  control_plane_poll_timeout?: string;
  mcp_server_url?: string;
  mcp_resource_metadata_urls?: string[];
  channels?: ChannelStatus[];
  control_plane_route?: ProxyRouteSummary;
  mcp_routes?: ProxyRouteSummary[];
  raw_http_logging_enabled?: boolean;
  tunnel_metadata?: TunnelMetadata;
  tunnel_metadata_error?: string;
  warnings?: string[];
}

export interface TunnelMetadata {
  name?: string;
  description?: string;
}

export interface ChannelStatus {
  name?: string;
  enabled?: boolean;
  server_kind?: string;
  transport_kind?: string;
  reason?: string;
  details?: ChannelDetail[];
}

export interface ChannelDetail {
  key?: string;
  value?: string;
}

export interface LogsResponse {
  events?: LogEvent[];
}

export interface LogEvent {
  seq?: number;
  time?: string;
  level?: string;
  message?: string;
  attrs?: Record<string, unknown>;
}

export interface OAuthStatusResponse {
  discovery_urls?: string[];
  metadata?: OAuthMetadata;
  error?: string;
  pending?: boolean;
  www_authenticate_probe?: OAuthProbe;
  metadata_source?: string;
  auth_server_metadata_mode?: string;
  authorization_server_count?: number;
  selected_authorization_server?: string;
}

export interface OAuthProbe {
  url?: string;
  error?: string;
}

export interface OAuthMetadata {
  fetched_at?: string;
  headers?: Record<string, string>;
  body?: unknown;
  body_text?: string;
  attempts?: OAuthAttempt[];
  auth_server_metadata?: OAuthAuthServerMetadata;
}

export interface OAuthAttempt {
  source?: string;
  url?: string;
  tried?: boolean;
  selected?: boolean;
  error?: string;
  status_code?: number;
  headers?: Record<string, string>;
  body?: unknown;
  body_text?: string;
}

export interface OAuthAuthServerMetadata {
  attempts?: OAuthAuthAttempt[];
}

export interface OAuthAuthAttempt {
  url?: string;
  tried?: boolean;
  selected?: boolean;
  error?: string;
  status_code?: number;
  headers?: Record<string, string>;
  body?: unknown;
  body_text?: string;
  document?: string;
  path_style?: string;
}

export interface OAuthRowDetails {
  statusCode?: number;
  status?: string;
  fetchedAt?: string;
  source?: string;
  sourceURL?: string;
  headers?: Record<string, string> | null;
  body?: unknown;
  bodyText?: string;
  error?: string;
}

export interface OAuthRow {
  key: string;
  priority: number;
  step: string;
  url: string;
  status: string;
  details: OAuthRowDetails;
}

export interface HarpoonStatusResponse {
  enabled?: boolean;
  reason?: string;
  capture_payloads?: boolean;
  allow_plaintext_http?: boolean;
  max_response_bytes?: number;
  max_redirects?: number;
  proxy_routes?: ProxyRouteSummary[];
}

export interface HarpoonTargetsResponse {
  targets?: HarpoonTarget[];
}

export interface HarpoonTarget {
  label?: string;
  url?: string;
  description?: string;
  category?: string;
  source?: string;
  tags?: string[];
  inclusion_reason?: string;
}

export interface HarpoonCallsResponse {
  calls?: HarpoonCall[];
}

export interface HarpoonCall {
  timestamp?: string;
  label?: string;
  url?: string;
  method?: string;
  status?: number;
  latency_ms?: number;
  req_bytes?: number;
  resp_bytes?: number;
  error?: string;
  request_body?: string;
  response_body?: string;
  response_body_transformed?: string;
  body_is_base64?: boolean;
}

export interface MetricSample {
  labels: string;
  value: number;
}

export type MetricMap = Map<string, MetricSample[]>;
