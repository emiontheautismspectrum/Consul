/**
 * Typed client for the Go backend (arcanum-api, /api/v1). Cookies carry the session
 * (HttpOnly, same origin through nginx or the Next rewrite). Server numbers are mapped onto
 * the cabinet's Ledger shape, so every screen keeps working unchanged: view(ledger) on the
 * mapped state reproduces the server's "can spend" exactly.
 */
import type { Debtor, Ledger, Money, Op, OpKind } from "./ledger";
import type { Action, Reply } from "./answer";

const BASE = "/api/v1";

export class ApiError extends Error {
  constructor(public status: number, public code: string, message: string, public field?: string) { super(message); }
  /** No response at all (offline, server down): the cabinet falls back to local demo mode. */
  get network() { return this.status === 0; }
}

type Opts = { body?: unknown; form?: FormData; idem?: boolean; timeout?: number; retried?: boolean; raw?: boolean };

const uuid = () => (typeof crypto !== "undefined" && "randomUUID" in crypto ? crypto.randomUUID() : `${Date.now()}-${Math.random().toString(36).slice(2)}`);

async function call<T>(method: string, path: string, o: Opts = {}): Promise<T> {
  const ctl = new AbortController();
  const timer = setTimeout(() => ctl.abort(), o.timeout ?? 8000);
  const headers: Record<string, string> = { Accept: "application/json" };
  if (o.body !== undefined) headers["Content-Type"] = "application/json";
  if (o.idem) headers["Idempotency-Key"] = uuid();
  let res: Response;
  try {
    res = await fetch(BASE + path, {
      method, headers, credentials: "include", signal: ctl.signal,
      body: o.form ?? (o.body !== undefined ? JSON.stringify(o.body) : undefined),
    });
  } catch {
    clearTimeout(timer);
    // GET is safe to repeat once; writes are not (the idempotency key changes).
    if (method === "GET" && !o.retried) return call<T>(method, path, { ...o, retried: true });
    throw new ApiError(0, "network", "network error");
  }
  clearTimeout(timer);
  // An expired access token: refresh once, then repeat the call.
  if (res.status === 401 && !o.retried && !path.startsWith("/auth/")) {
    const r = await fetch(BASE + "/auth/refresh", { method: "POST", credentials: "include" }).catch(() => null);
    if (r?.ok) return call<T>(method, path, { ...o, retried: true });
  }
  if (!res.ok) {
    let e: { error?: { code?: string; message?: string; field?: string } } = {};
    try { e = await res.json(); } catch { /* not JSON (nginx page) */ }
    throw new ApiError(res.status, e.error?.code || `http_${res.status}`, e.error?.message || res.statusText, e.error?.field);
  }
  if (o.raw) return res as unknown as T;
  if (res.status === 204) return undefined as T;
  return res.json() as Promise<T>;
}

// ---- types (backend/api/openapi.yaml) ----

export interface ApiView {
  cash: number; naqd: number; karta: number; tax: number; obligations: number; reserve: number; free: number;
  profit: number; margin: number; owed_to_me: number; debtor_count: number; profit_3m: number;
}
export interface ApiUpcoming { kind: "rent" | "supplier" | "loan" | "other" | "tax"; label: string; amount: number; due_date: string; days_left: number }
export interface ApiToday {
  view: ApiView; upcoming: ApiUpcoming[]; attention: { code: string; severity: string; params: Record<string, unknown> }[];
  forecast: { date: string; balance: number }[];
  month: { month: string; income: number; buy: number; exp: number; profit: number; margin: number; prev_income: number; prev_profit: number; ytd_before: number };
}
export interface ApiOp {
  id: string; kind: OpKind | "fix" | "opening"; amount: number; method: "naqd" | "karta" | "none"; label: string;
  counterparty: string | null; fiscal: boolean; source: string; occurred_at: string;
}
export interface ApiDebtor { id: string; name: string; owed: number; days: number }
export interface Draft { kind?: ApiOp["kind"]; amount?: number; method?: ApiOp["method"]; label?: string; counterparty?: string; source?: string; raw_text?: string }
export interface ApiIntent {
  type: "op" | "fix" | "rent" | "question" | "report" | "need_amount" | "need_kind" | "unknown";
  draft?: Draft; rent?: { amount: number; due_day?: number }; topic?: string; lang: "uz" | "ru"; confirm: boolean;
}
export interface OpResult { op: ApiOp; free_before: number; free_after: number }
export interface Business { trade: string; sector: string; market: string; status: string; regime: string; reserve: number }
export interface Profile {
  id: string; phone: string | null; email: string | null; role: string; lang: "uz" | "ru" | "en";
  prefs: { big_text?: boolean; voice_answers?: boolean; voice_speed?: number };
  consent_at: string | null; stats_consent_at: string | null; business: Business | null;
}
/** The backend Reply is the frontend Reply plus chat metadata. */
export interface ApiReply extends Omit<Reply, "actions"> {
  actions?: (Omit<Action, "run"> & { run?: string })[]; topic: string; chat_id: string;
  pending_op?: Draft | null; rent?: { amount: number; due_day?: number } | null;
}
export interface PalataGroup { key: string; n: number; unrecorded_pct: number; official_pct: number; median_turnover: number; switched_to_official: number }
export interface PalataReport {
  window: 30 | 90; period_end: string; synthetic: boolean;
  kpi: { n: number; unrecorded_pct: number; unrecorded_ci_pp: number; official_pct: number; switched_to_official: number; median_turnover: number };
  trend: { month: string; unrecorded_pct: number; official_pct?: number; n: number }[];
  sectors: PalataGroup[]; markets: PalataGroup[];
  survey: { n: number; answers: { answer: string; pct: number }[] };
}
export interface BankReport {
  generated_at: string; business: Business; avg_profit: number; tax_this_month: number; owed_to_me: number; owed_to_suppliers: number; cash: number;
  months: { month: string; income: number; expense: number; profit: number; partial: boolean }[];
}
export type WhatIfInput =
  | { kind: "buy"; amount?: number; payments?: { day: number; amount: number }[] }
  | { kind: "helper"; amount: number; uplift_pct?: number }
  | { kind: "loan"; amount: number; annual_rate?: number; term_months: number };
export interface WhatIfResult {
  baseline: { date: string; balance: number }[]; scenario: { date: string; balance: number }[];
  lowest: { date: string; balance: number }; lowest_day: number; verdict: "ok" | "tight" | "short"; assumptions: string[];
}
export interface PlanResult {
  cogs_pct: number; estimated: boolean;
  base: { profit: number; payback_months: number | null }; sales_minus_20: { profit: number; payback_months: number | null }; rent_plus_20: { profit: number; payback_months: number | null };
}
export interface ChatSummary { id: string; title: string; topic: string; updated_at: string }
export interface ChatMessage { role: "user" | "assistant"; text: string; reply?: ApiReply; created_at: string }
export interface Recognized {
  is_document: boolean; document_type: string; title: string; text: string; summary: string; warnings: string[];
  fields: { key: string; label: string; value: string }[];
  draft_ops?: Draft[];
}

// ---- endpoints ----

export const api = {
  me: () => call<Profile>("GET", "/me"),
  patchMe: (p: { lang?: string; prefs?: Profile["prefs"] }) => call<Profile>("PATCH", "/me", { body: p }),
  putBusiness: (b: Business) => call<Profile>("PUT", "/me/business", { body: b }),
  consent: () => call<Profile>("POST", "/me/consent"),
  statsConsent: (on: boolean) => call<Profile>(on ? "POST" : "DELETE", "/me/consent/stats"),

  otpSend: (phone: string) => call<void>("POST", "/auth/otp/send", { body: { phone } }),
  otpVerify: (phone: string, code: string, lang: string) => call<{ user: Profile; is_new: boolean }>("POST", "/auth/otp/verify", { body: { phone, code, lang } }),
  demo: () => call<{ user: Profile }>("POST", "/auth/demo", { timeout: 15000 }),
  logout: () => call<void>("POST", "/auth/logout"),

  today: () => call<ApiToday>("GET", "/ledger/today"),
  ops: (limit = 40) => call<{ items: ApiOp[]; next_cursor?: string }>("GET", `/ops?limit=${limit}`),
  debtors: () => call<{ items: ApiDebtor[]; total: number }>("GET", "/debtors"),
  parse: (text: string) => call<ApiIntent>("POST", "/ops/parse", { body: { text } }),
  addOp: (d: Draft) => call<OpResult>("POST", "/ops", { body: d, idem: true }),
  undo: (id: string) => call<{ free_before: number; free_after: number }>("DELETE", `/ops/${encodeURIComponent(id)}`),
  repaid: (id: string) => call<OpResult>("POST", `/debtors/${encodeURIComponent(id)}/repaid`, { body: {}, idem: true }),
  obligations: () => call<{ items: { id: string; kind: string; label: string; amount: number; due_day?: number }[] }>("GET", "/obligations"),
  addObligation: (o: { kind: string; label: string; amount: number; due_day?: number; recurring?: boolean }) => call<{ id: string }>("POST", "/obligations", { body: o }),
  patchObligation: (id: string, o: { kind: string; label: string; amount: number; due_day?: number; recurring?: boolean }) =>
    call<{ id: string }>("PATCH", `/obligations/${encodeURIComponent(id)}`, { body: o }),

  ask: (text: string, chatId: string | null, route: string) =>
    call<ApiReply>("POST", "/consul/ask", { body: { text, chat_id: chatId || null, route }, idem: true, timeout: 25000 }),
  stt: (audio: Blob, lang: string) => { const f = new FormData(); f.append("audio", audio, "voice"); f.append("lang", lang); return call<{ text: string; lang: string }>("POST", "/voice/stt", { form: f, timeout: 25000 }); },
  tts: (text: string, lang: string, speed = 1) => call<Response>("POST", "/voice/tts", { body: { text, lang, speed }, timeout: 20000, raw: true }),
  photo: (file: Blob, lang: string) => { const f = new FormData(); f.append("file", file, "photo"); f.append("lang", lang); return call<Recognized>("POST", "/photo", { form: f, timeout: 70000 }); },
  bankReport: (months = 3) => call<BankReport>("GET", `/reports/bank?months=${months}`),
  whatIf: (w: WhatIfInput) => call<WhatIfResult>("POST", "/forecast/whatif", { body: w }),
  plan: (p: { capital: number; sales: number; rent: number; helper: number }) => call<PlanResult>("POST", "/advisor/plan", { body: p }),
  chats: () => call<{ items: ChatSummary[] }>("GET", "/chats?limit=30"),
  chatMessages: (id: string) => call<{ items: ChatMessage[] }>("GET", `/chats/${encodeURIComponent(id)}/messages`),
  memory: () => call<{ items: { key: string; value: string }[] }>("GET", "/memory"),
  putFact: (key: string, value: string) => call<{ items: { key: string; value: string }[] }>("PUT", "/memory", { body: { key, value } }),
  forget: (key: string) => call<void>("DELETE", `/memory/${encodeURIComponent(key)}`),
  forgetAll: () => call<void>("DELETE", "/memory"),
  survey: (answer: string) => call<void>("POST", "/me/survey", { body: { question: "why_not_registered", answer } }),
  deleteMe: () => call<void>("DELETE", "/me?confirm=delete"),
  exportUrl: `${BASE}/me/export.csv`,
  palata: (window: 30 | 90) => call<PalataReport>("GET", `/palata/report?window=${window}`),
};

// ---- server state → cabinet Ledger ----

const MONTHS = ["yanvar", "fevral", "mart", "aprel", "may", "iyun", "iyul", "avgust", "sentabr", "oktabr", "noyabr", "dekabr"];
const TZ = "Asia/Tashkent";
const ymd = (d: Date) => new Intl.DateTimeFormat("en-CA", { timeZone: TZ, year: "numeric", month: "2-digit", day: "2-digit" }).format(d);
const cap = (s: string) => s.charAt(0).toUpperCase() + s.slice(1);

/** "Bugun", "Kecha" or "15-noyabr" in Tashkent time (the frontend groups ops by this label). */
export function dayLabel(iso: string, now = new Date()): string {
  const d = new Date(iso), day = ymd(d), today = ymd(now), yesterday = ymd(new Date(now.getTime() - 86_400_000));
  if (day === today) return "Bugun";
  if (day === yesterday) return "Kecha";
  const [, m, dd] = day.split("-").map(Number);
  return `${dd}-${MONTHS[m - 1]}`;
}

/** Same titles as apply() in ledger.ts, so translations and icons keep working. */
export function opTitle(o: ApiOp): string {
  const who = o.counterparty || "";
  switch (o.kind) {
    case "sale": return "Sotuv";
    case "buy": return o.label ? `Xarid · ${o.label}` : "Xarid";
    case "exp": return cap(o.label || "xarajat");
    case "sup": return "Taʼminotchiga toʻlov";
    case "nas": return `Nasiya · ${who || "mijoz"}`;
    case "repaid": return `Nasiya qaytdi · ${who}`;
    case "fix": return "Tuzatish";
    case "opening": return "Boshlangʻich qoldiq";
  }
}

const sign = (o: ApiOp) => (o.kind === "sale" || o.kind === "repaid" || o.kind === "opening" ? 1 : o.kind === "nas" ? 0 : o.kind === "fix" ? Math.sign(o.amount) : -1);

export function toOp(o: ApiOp, now = new Date()): Op {
  const time = new Intl.DateTimeFormat("ru-RU", { timeZone: TZ, hour: "2-digit", minute: "2-digit" }).format(new Date(o.occurred_at));
  const amount = o.kind === "fix" ? o.amount : sign(o) * o.amount;
  const kind = (["sale", "buy", "exp", "sup", "nas", "repaid"] as const).includes(o.kind as OpKind) ? (o.kind as OpKind) : undefined;
  return { id: o.id, day: dayLabel(o.occurred_at, now), title: opTitle(o), sub: o.method === "none" ? time : `${time} · ${o.method}`, amount, kind };
}

/**
 * Maps the server's Today onto Money so that view() gives the same numbers:
 * obligations = rent (+loan, other) + supplier + round(income × taxRate) = server obligations.
 */
export function toLedger(t: ApiToday, ops: ApiOp[], debtors: ApiDebtor[], memory: Ledger["memory"], checks?: Ledger["checks"]): Ledger {
  const v = t.view, m = t.month;
  const sum = (kinds: ApiUpcoming["kind"][]) => t.upcoming.filter(u => kinds.includes(u.kind)).reduce((s, u) => s + u.amount, 0);
  const money: Money = {
    naqd: v.naqd, karta: v.karta, rent: sum(["rent", "loan", "other"]), supplier: sum(["supplier"]),
    income: m.income, buy: m.buy, other: m.exp, taxRate: m.income ? v.tax / m.income : 0.01, ytd: m.ytd_before, reserve: v.reserve,
  };
  const ds: Debtor[] = debtors.map(d => ({ name: d.name, amount: d.owed, days: d.days }));
  return { money, debtors: ds, ops: ops.map(o => toOp(o)), memory, checks, profit3m: v.profit_3m };
}

export async function loadLedger(memory: Ledger["memory"], checks?: Ledger["checks"]) {
  const [t, o, d] = await Promise.all([api.today(), api.ops(), api.debtors()]);
  return { ledger: toLedger(t, o.items, d.items, memory, checks), debtorIds: Object.fromEntries(d.items.map(x => [x.name, x.id])) as Record<string, string>, today: t };
}
