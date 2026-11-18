"use client";
import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { DEMO, USER, type Persona } from "@/lib/demo";
import { apply, view, type Ledger, type View } from "@/lib/ledger";
import { intent, norm } from "@/lib/parse";
import { answer, type Reply, type Topic } from "@/lib/answer";
import { DEFAULT_PLAN, type Plan } from "@/lib/plan";
import { fmt } from "@/lib/format";
import { useT, useTP } from "@/components/I18n";
import { recordOperation, type OperationInput } from "@/lib/operations";
import { readLedger, LEDGER_STORAGE_KEY } from "@/lib/storage";
import { api, ApiError, dayLabel, loadLedger, type ApiReply, type Draft } from "@/lib/api";

export interface Toast { id: number; main: string; sub?: string; warn?: boolean; undo?: () => void }
export interface Msg { id: number; role: "user" | "bot"; text?: string; reply?: Reply; thinking?: boolean; voice?: boolean; pending?: Draft | null }
export interface ChatItem { id: string; title: string; date: string; group: string; topic: string }

interface Store {
  ledger: Ledger; view: View; route: string; setRoute: (r: string) => void;
  toast: Toast | null; showToast: (t: Omit<Toast, "id">) => void; hideToast: () => void;
  sheet: ReactNode | null; openSheet: (n: ReactNode) => void; closeSheet: () => void;
  handle: (text: string, opts?: { voice?: boolean }) => void;
  applyFixed: (kind: Parameters<typeof apply>[1], text: string, amount: number) => void;
  addOperation: (input: OperationInput) => void;
  repaid: (name: string) => void;
  forceAct: (text: string) => void;
  setReserve: (v: number) => void;
  setMemory: (m: [string, string][]) => void;
  /** Opens a saved server conversation (remote mode). */
  openServerChat: (h: ChatItem) => void;
  chatId: string; chatTitle: string; loadChat: (h: ChatItem, q: string, r: Reply, t: Topic) => void;
  msgs: Msg[]; chat: (text: string, opts?: { voice?: boolean }) => void; newChat: () => void;
  history: ChatItem[]; setTopic: (t: Topic) => void;
  plan: Plan; setPlan: (p: Plan) => void;
  calcTab: string; setCalcTab: (t: string) => void;
  bigText: boolean; setBigText: (b: boolean) => void;
  stats: boolean; setStats: (b: boolean) => void;
  /** Who is signed in: the demo persona offline, the real profile from the server otherwise. */
  persona: Persona;
  ui: UiHooks; setUi: (u: UiHooks) => void;
  /** true when numbers come from the server; false = local demo (not logged in or offline). */
  remote: boolean;
  /** Records a draft the assistant prepared (pending_op) after the user confirmed it. */
  confirmDraft: (d: Draft) => void;
  /** Saves the monthly rent the assistant understood (run: add_rent). */
  confirmRent: (r: { amount: number; due_day?: number }) => void;
  lang: string;
}
/** Sheets that live in components and are registered by the shell (avoids import cycles). */
export interface UiHooks { answerSheet?: (q: string, r: Reply) => ReactNode; chooseKind?: (t: string, a: number) => ReactNode; bigConfirm?: (t: string, a: number) => ReactNode; bankReport?: () => ReactNode }

const Ctx = createContext<Store | null>(null);
export const useStore = () => { const s = useContext(Ctx); if (!s) throw new Error("useStore outside provider"); return s; };

const HISTORY: ChatItem[] = [
  { id: "c1", title: "Kredit 34 mln", date: "kecha", group: "Shu hafta", topic: "loan" },
  { id: "c2", title: "Soliq rejimi", date: "12-noyabr", group: "Oldinroq", topic: "tax" },
  { id: "c3", title: "Qoqi narxi", date: "9-noyabr", group: "Oldinroq", topic: "price" },
];

export function StoreProvider({ children }: { children: ReactNode }) {
  const t = useT(), tp = useTP();
  const lang = typeof document !== "undefined" && document.documentElement.lang === "ru" ? "ru" : "uz";
  const [remote, setRemote] = useState(false);
  const remoteRef = useRef(false);
  const debtorIds = useRef<Record<string, string>>({});
  const serverChat = useRef<string | null>(null);
  // Resolves once we know whether the user is logged in. Actions wait for it: otherwise a
  // phrase typed in the first moments would be recorded only locally (the demo numbers on
  // screen are the same as the server's, so nobody would notice).
  const [gate] = useState(() => { let open = () => {}; const done = new Promise<void>(r => { open = r; }); return { done, open }; });

  const [ledger, setLedgerState] = useState<Ledger>(DEMO);
  const [storageReady, setStorageReady] = useState(false);
  const [route, setRoute] = useState("bugun");
  const [toast, setToast] = useState<Toast | null>(null);
  const [sheet, setSheet] = useState<ReactNode | null>(null);
  const [msgs, setMsgs] = useState<Msg[]>([]);
  const [history, setHistory] = useState<ChatItem[]>(HISTORY);
  const [chatId, setChatId] = useState("");
  const [chatTitle, setChatTitle] = useState("");
  const [plan, setPlan] = useState<Plan>(DEFAULT_PLAN);
  const [calcTab, setCalcTab] = useState("kredit");
  const [bigText, setBigText] = useState(false);
  const [stats, setStats] = useState(true);
  const [persona, setPersona] = useState<Persona>(USER);
  const [ui, setUi] = useState<UiHooks>({});
  const topic = useRef<Topic>(null);
  const ledgerRef = useRef(ledger);
  const setLedger = useCallback((next: Ledger | ((current: Ledger) => Ledger)) => {
    const value = typeof next === "function" ? next(ledgerRef.current) : next;
    ledgerRef.current = value;
    setLedgerState(value);
  }, []);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const v = useMemo(() => view(ledger), [ledger]);

  const showToast = useCallback((t: Omit<Toast, "id">) => {
    setToast({ ...t, id: Date.now() });
    clearTimeout(timer.current);
    timer.current = setTimeout(() => setToast(null), 7000);
  }, []);
  const hideToast = useCallback(() => setToast(null), []);
  const openSheet = useCallback((n: ReactNode) => setSheet(n), []);
  const closeSheet = useCallback(() => setSheet(null), []);

  /** Saved conversations, grouped like the demo list (today, this week, earlier). */
  const loadHistory = useCallback(() => {
    api.chats().then(r => {
      const now = new Date();
      setHistory(r.items.map(c => {
        const age = (now.getTime() - new Date(c.updated_at).getTime()) / 86_400_000;
        return { id: c.id, title: c.title || "Suhbat", date: dayLabel(c.updated_at, now).toLowerCase(), group: age < 1 ? "Bugun" : age < 7 ? "Shu hafta" : "Oldinroq", topic: c.topic };
      }));
    }).catch(() => {});
  }, []);

  /** Reloads the ledger from the server; local memory and checklist stay as they are. */
  const refresh = useCallback(async () => {
    const cur = ledgerRef.current;
    const r = await loadLedger(cur.memory, cur.checks);
    debtorIds.current = r.debtorIds;
    setLedger(r.ledger);
  }, [setLedger]);

  /** The server is unreachable: keep working on the local copy and say so. */
  const goOffline = useCallback(() => {
    if (!remoteRef.current) return;
    remoteRef.current = false; setRemote(false);
    showToast({ main: t("Server bilan aloqa yoʻq"), sub: t("Demo rejimida davom etamiz. Yozuvlar serverga tushmaydi."), warn: true });
  }, [showToast, t]);

  /** Shows an API failure; network problems switch to local demo mode. */
  const fail = useCallback((e: unknown) => {
    if (e instanceof ApiError && e.network) return goOffline();
    showToast({ main: t("Saqlab boʻlmadi"), sub: e instanceof ApiError ? e.code : String(e), warn: true });
  }, [goOffline, showToast, t]);

  useEffect(() => {
    // Logged in → server numbers. Otherwise restore the local demo after hydration so the
    // server and the first client render use identical demo data.
    let active = true;
    (async () => {
      try {
        const me = await api.me();
        if (!active) return;
        remoteRef.current = true; setRemote(true);
        setBigText(!!me.prefs?.big_text); setStats(!!me.stats_consent_at);
        setPersona({ name: "", full: me.phone || me.email || "", trade: me.business?.trade || "", place: me.business?.market && me.business.market !== "other" ? me.business.market : "", status: (me.business?.status || "").toUpperCase() });
        await refresh();
        // What Consul remembers, the saved chats and the cost share from the user's own history.
        api.memory().then(m => { if (active) setLedger(l => ({ ...l, memory: m.items.map(f => [f.key, f.value] as [string, string]) })); }).catch(() => {});
        loadHistory();
        api.plan({ capital: DEFAULT_PLAN.capital, sales: DEFAULT_PLAN.sales, rent: DEFAULT_PLAN.rent, helper: DEFAULT_PLAN.helper })
          .then(p => { if (active) setPlan(x => ({ ...x, cogs: p.cogs_pct / 100, estimated: p.estimated })); }).catch(() => {});
      } catch {
        if (!active) return;
        const saved = readLedger();
        if (saved) setLedger(saved);
      }
      if (active) setStorageReady(true);
      gate.open();
    })();
    return () => { active = false; };
  }, [setLedger, refresh, gate, loadHistory]);
  const storageFailed = useRef(false);
  useEffect(() => {
    if (!storageReady || remoteRef.current) return; // the server owns the ledger in remote mode
    try { localStorage.setItem(LEDGER_STORAGE_KEY, JSON.stringify(ledger)); }
    catch {
      if (!storageFailed.current) showToast({ main: t("Brauzerda saqlab boʻlmadi"), sub: t("Yozuvlar faqat joriy seansda qoladi."), warn: true });
      storageFailed.current = true;
    }
  }, [ledger, storageReady, showToast, t]);

  /** Server write + toast with the change in free money and a real undo (DELETE /ops/{id}). */
  const record = useCallback(async (d: Draft, warnIfNegative = true) => {
    const r = await api.addOp({ source: "text", ...d });
    await refresh();
    const op = ledgerRef.current.ops.find(o => o.id === r.op.id);
    const title = op ? `${tp(op.title)} ${fmt(Math.abs(op.amount))}` : t("Operatsiya saqlandi");
    showToast({
      main: title, sub: t("Sarflash mumkin: {a} → {b}", { a: fmt(r.free_before), b: fmt(r.free_after) }) + (r.free_after < 0 && warnIfNegative ? " · " + t("erkin pul yetmayapti") : ""),
      warn: r.free_after < 0, undo: () => { api.undo(r.op.id).then(refresh).catch(fail); },
    });
    return t("Yozdim: {op}. Sarflash mumkin: {a} → {b}.", { op: title, a: fmt(r.free_before), b: fmt(r.free_after) });
  }, [refresh, showToast, t, tp, fail]);

  /** Text-to-speech for voice questions; silent when the voice provider is unavailable. */
  const speak = useCallback((text: string) => {
    api.tts(text, lang).then(r => r.blob()).then(b => { const a = new Audio(URL.createObjectURL(b)); void a.play().catch(() => {}); }).catch(() => {});
  }, [lang]);

  /** Upserts the monthly rent obligation and mirrors it in Consul's memory. */
  const saveRent = useCallback(async (rent: { amount: number; due_day?: number }) => {
      const o = { kind: "rent", label: "ijara", amount: rent.amount, due_day: rent.due_day || 5, recurring: true };
      const ex = (await api.obligations()).items.find(x => x.kind === "rent");
      await (ex ? api.patchObligation(ex.id, o) : api.addObligation(o));
      const val = t("{a} · har oy {d}-sanada", { a: fmt(o.amount), d: o.due_day });
      setLedger(l => ({ ...l, memory: [...l.memory.filter(x => x[0] !== "Ijara"), ["Ijara", val]] }));
      await refresh();
      showToast({ main: t("Eslab qoldim: ijara {v}", { v: val }), sub: t("Consul xotirasida koʻrinadi") });
  }, [refresh, setLedger, showToast, t]);

  const addOperation = useCallback((input: OperationInput) => {
    if (remoteRef.current) {
      record({ kind: input.kind, amount: input.amount, method: input.kind === "nas" ? "none" : input.method,
        label: input.kind === "exp" ? input.category : input.note?.trim().slice(0, 160) || undefined, counterparty: input.who?.trim() || undefined, source: "manual" }).catch(fail);
      return;
    }
    const before = ledgerRef.current, result = recordOperation(before, input);
    setLedger(result.ledger);
    showToast({ main: t("Operatsiya saqlandi"), sub: `${tp(result.title)} ${result.value}`, undo: () => setLedger(before) });
  }, [setLedger, showToast, t, tp, fail, record]);

  /** Applies an operation, shows the change in free money, keeps an undo snapshot. Returns a chat-friendly line. */
  const act = useCallback((text: string, quiet = false, force = false): string | null => {
    const l = ledgerRef.current, nt = norm(text), it = intent(text, l.debtors);
    const undo = () => setLedger(l);
    if (it.type === "fix") {
      const was = l.money[it.field];
      const next = { ...l, money: { ...l.money, [it.field]: it.amount } };
      setLedger(next);
      const msg = t(it.field === "naqd" ? "Naqd tuzatildi: {a} → {b}" : "Karta tuzatildi: {a} → {b}", { a: fmt(was), b: fmt(it.amount) });
      if (!quiet) showToast({ main: msg, sub: t("Sarflash mumkin: {x}", { x: fmt(view(next).free) }), undo });
      return msg + ".";
    }
    if (it.type === "rent") {
      const val = t("{a} · har oy {d}-sanada", { a: fmt(it.amount || l.money.rent), d: it.day });
      const mem = l.memory.some(x => x[0] === "Ijara") ? l.memory.map(x => (x[0] === "Ijara" ? ["Ijara", val] as [string, string] : x)) : [...l.memory, ["Ijara", val] as [string, string]];
      setLedger({ ...l, memory: mem, money: it.amount ? { ...l.money, rent: it.amount } : l.money });
      if (!quiet) showToast({ main: t("Eslab qoldim: ijara {v}", { v: val }), sub: t("Consul xotirasida koʻrinadi") });
      return t("Eslab qoldim: ijara {v}", { v: val }) + ".";
    }
    if (it.type !== "op") return null;
    if (it.amount >= 1e9 && !force) {
      if (quiet) return t("{a} soʻm — juda katta summa. Toʻgʻri boʻlsa, pastdagi qatorda qayta yozing.", { a: fmt(it.amount) });
      if (ui.bigConfirm) setSheet(ui.bigConfirm(text, it.amount));
      return null;
    }
    const r = apply(l, it.kind, it.amount, it.method, it.label, it.who);
    setLedger(r.ledger);
    if (!quiet) showToast({ main: `${tp(r.title)} ${r.value}`, sub: t("Sarflash mumkin: {a} → {b}", { a: fmt(r.before), b: fmt(r.after) }) + (r.after < 0 ? " · " + t("erkin pul yetmayapti") : ""), undo, warn: r.after < 0 });
    void nt;
    return t("Yozdim: {op}. Sarflash mumkin: {a} → {b}.", { op: `${tp(r.title)} ${r.value}`, a: fmt(r.before), b: fmt(r.after) });
  }, [setLedger, showToast, ui, t, tp]);

  const ask = useCallback((text: string) => {
    const l = ledgerRef.current;
    const res = answer(text, { ledger: l, view: view(l), route, last: topic.current, t });
    topic.current = res.topic;
    return res.reply;
  }, [route, t]);

  const chat = useCallback((text: string, opts?: { voice?: boolean }) => {
    const idU = Date.now(), idB = idU + 1;
    setMsgs(m => [...m, { id: idU, role: "user", text }, { id: idB, role: "bot", thinking: true }]);
    setHistory(h => (h.some(x => x.id === "live") ? h : [{ id: "live", title: text.slice(0, 26), date: "hozir", group: "Bugun", topic: "" }, ...h]));
    setChatId(c => c || "live");
    if (remoteRef.current) {
      api.ask(text, serverChat.current, "/app/consul").then((r: ApiReply) => {
        serverChat.current = r.chat_id;
        setChatId(r.chat_id); loadHistory();
        setMsgs(m => m.map(x => (x.id === idB ? { ...x, thinking: false, reply: r as Reply, voice: opts?.voice, pending: r.pending_op } : x)));
        if (opts?.voice) speak(r.text);
      }).catch(e => {
        setMsgs(m => m.filter(x => x.id !== idB));
        fail(e);
      });
      return;
    }
    setTimeout(() => {
      const l = ledgerRef.current, it = intent(text, l.debtors);
      let reply: Reply;
      if (it.type === "op" || it.type === "fix" || it.type === "rent") reply = { text: act(text, true) || t("Yozib qoʻydim.") };
      else reply = ask(text);
      setMsgs(m => m.map(x => (x.id === idB ? { ...x, thinking: false, reply, voice: opts?.voice } : x)));
    }, 700);
  }, [act, ask, t, fail, speak, loadHistory]);

  /** Remote: the server parser decides, the server ledger records, the server chat answers. */
  const handleRemote = useCallback(async (q: string, force = false) => {
    const it = await api.parse(q);
    if (it.type === "report") { if (ui.bankReport) setSheet(ui.bankReport()); return; }
    if (it.type === "need_kind" && it.draft?.amount) { if (ui.chooseKind) setSheet(ui.chooseKind(q, it.draft.amount)); return; }
    if (it.type === "need_amount") return showToast({ main: t("Summani ham ayting"), sub: t("Masalan: «1,2 mln sotdim», «svet 420 ming»"), warn: true });
    if ((it.type === "op" || it.type === "fix") && it.draft) {
      if (it.confirm && !force) { if (ui.bigConfirm) setSheet(ui.bigConfirm(q, it.draft.amount || 0)); return; }
      await record(it.draft);
      return;
    }
    if (it.type === "rent" && it.rent?.amount) {
      await saveRent(it.rent);
      return;
    }
    // question / unknown → Consul
    const r = await api.ask(q, serverChat.current, "/app" + (route === "bugun" ? "" : "/" + route));
    serverChat.current = r.chat_id;
    if (ui.answerSheet) setSheet(ui.answerSheet(q, r as Reply));
  }, [ui, record, saveRent, showToast, t, route]);


  const handle = useCallback((text: string, opts?: { voice?: boolean }) => {
    const q = text.trim(); if (!q) return;
    if (typeof document !== "undefined") (document.activeElement as HTMLElement | null)?.blur?.();
    if (route === "consul") return chat(q, opts);
    if (remoteRef.current) { handleRemote(q).catch(fail); return; }
    const l = ledgerRef.current, it = intent(q, l.debtors);
    if (it.type === "report") { if (ui.bankReport) setSheet(ui.bankReport()); return; }
    if (it.type === "question") { if (ui.answerSheet) setSheet(ui.answerSheet(q, ask(q))); return; }
    if (it.type === "op" || it.type === "fix" || it.type === "rent") { act(q); return; }
    if (it.type === "need-kind") { if (ui.chooseKind) setSheet(ui.chooseKind(q, it.amount)); return; }
    if (it.type === "need-amount") return showToast({ main: t("Summani ham ayting"), sub: t("Masalan: «1,2 mln sotdim», «svet 420 ming»"), warn: true });
    const r = ask(q);
    if (!r.fallback && ui.answerSheet) return setSheet(ui.answerSheet(q, r));
    showToast({ main: t("Tushunmadim"), sub: t("Masalan: «1,2 mln sotdim», «Karimga 500 ming nasiya», «kredit olsam boʻladimi?»"), warn: true });
  }, [route, chat, act, ask, showToast, ui, t, fail, handleRemote]);

  const applyFixed = useCallback((kind: Parameters<typeof apply>[1], text: string, amount: number) => {
    if (remoteRef.current) {
      record({ kind, amount, method: kind === "nas" ? "none" : /karta|карт/.test(norm(text)) ? "karta" : "naqd", label: kind === "buy" ? "qoqi, mayiz" : undefined, raw_text: text.slice(0, 500) }).catch(fail);
      return;
    }
    const l = ledgerRef.current, r = apply(l, kind, amount, /karta/.test(norm(text)) ? "karta" : "naqd", kind === "buy" ? "qoqi, mayiz" : undefined);
    setLedger(r.ledger);
    showToast({ main: `${tp(r.title)} ${r.value}`, sub: t("Sarflash mumkin: {a} → {b}", { a: fmt(r.before), b: fmt(r.after) }), undo: () => setLedger(l), warn: r.after < 0 });
  }, [setLedger, showToast, t, tp, fail, record]);

  const repaid = useCallback((name: string) => {
    if (remoteRef.current) {
      const id = debtorIds.current[name];
      if (!id) return;
      api.repaid(id).then(async r => {
        await refresh();
        showToast({ main: t("{n} toʻladi {v}", { n: name, v: "+" + fmt(r.op.amount) }), sub: t("Sarflash mumkin: {x}", { x: fmt(r.free_after) }),
          undo: () => { api.undo(r.op.id).then(refresh).catch(fail); } });
      }).catch(fail);
      return;
    }
    const l = ledgerRef.current, r = apply(l, "repaid", 0, "naqd", undefined, name);
    setLedger(r.ledger);
    showToast({ main: t("{n} toʻladi {v}", { n: name, v: r.value }), sub: t("Sarflash mumkin: {x}", { x: fmt(r.after) }), undo: () => setLedger(l) });
  }, [setLedger, showToast, t, fail, refresh]);


  const confirmDraft = (d: Draft) => { record(d).catch(fail); };
  const confirmRent = (r: { amount: number; due_day?: number }) => { saveRent(r).catch(fail); };

  const setReserve = (r: number) => {
    const v = Math.max(0, r);
    if (!remoteRef.current) return setLedger(l => ({ ...l, money: { ...l.money, reserve: v } }));
    api.me().then(me => api.putBusiness({ ...(me.business || { trade: "savdo", sector: "other", market: "other", status: "yatt", regime: "turnover", reserve: 0 }), reserve: v }))
      .then(refresh).catch(fail);
  };
  const setStatsRemote = (on: boolean) => { setStats(on); if (remoteRef.current) api.statsConsent(on).catch(fail); };
  const setBigTextRemote = (on: boolean) => { setBigText(on); if (remoteRef.current) api.patchMe({ prefs: { big_text: on } }).catch(fail); };

  // Gated entry points (see `gate`): they run once the session check has finished.
  const handleG = useCallback((q: string, o?: { voice?: boolean }) => { void gate.done.then(() => handle(q, o)); }, [gate, handle]);
  const chatG = useCallback((q: string, o?: { voice?: boolean }) => { void gate.done.then(() => chat(q, o)); }, [gate, chat]);
  const applyFixedG = useCallback((k: Parameters<typeof apply>[1], q: string, a: number) => { void gate.done.then(() => applyFixed(k, q, a)); }, [gate, applyFixed]);
  const addOperationG = useCallback((i: OperationInput) => { void gate.done.then(() => addOperation(i)); }, [gate, addOperation]);
  const repaidG = useCallback((n: string) => { void gate.done.then(() => repaid(n)); }, [gate, repaid]);
  const forceActG = useCallback((q: string) => {
    void gate.done.then(() => { if (remoteRef.current) handleRemote(q, true).catch(fail); else act(q, false, true); });
  }, [gate, handleRemote, fail, act]);

  const openServerChat = useCallback((h: ChatItem) => {
    api.chatMessages(h.id).then(r => {
      serverChat.current = h.id;
      setChatId(h.id); setChatTitle(`${h.title} · ${h.date}`);
      setMsgs(r.items.map((m, i) => (m.role === "user" ? { id: i + 1, role: "user" as const, text: m.text } : { id: i + 1, role: "bot" as const, reply: (m.reply as Reply) || { text: m.text } })));
    }).catch(fail);
  }, [fail]);

  /** Memory edits go to the server as a diff against what is shown. */
  const setMemory = (next: [string, string][]) => {
    const prev = ledgerRef.current.memory;
    setLedger(l => ({ ...l, memory: next }));
    if (!remoteRef.current) return;
    void gate.done.then(async () => {
      try {
        if (next.length === 0) { await api.forgetAll(); return; }
        for (const [k] of prev) if (!next.some(n => n[0] === k)) await api.forget(k);
        for (const [k, v] of next) if (prev.find(p => p[0] === k)?.[1] !== v) await api.putFact(k, v);
      } catch (e) { fail(e); }
    });
  };

  const value: Store = {
    ledger, view: v, route, setRoute, toast, showToast, hideToast, sheet, openSheet, closeSheet,
    handle: handleG, applyFixed: applyFixedG, addOperation: addOperationG, repaid: repaidG, forceAct: forceActG,
    setReserve: (r) => { void gate.done.then(() => setReserve(r)); },
    setMemory, openServerChat,
    chatId, chatTitle, loadChat: (h, q, r, t) => { setChatId(h.id); setChatTitle(`${h.title} · ${h.date}`); setMsgs([{ id: 1, role: "user", text: q }, { id: 2, role: "bot", reply: r }]); topic.current = t; },
    msgs, chat: chatG, newChat: () => { setMsgs([]); setChatId(""); setChatTitle(""); topic.current = null; serverChat.current = null; }, history, setTopic: (t) => { topic.current = t; },
    persona, plan, setPlan, calcTab, setCalcTab, bigText, setBigText: setBigTextRemote, stats, setStats: setStatsRemote, ui, setUi,
    remote, confirmDraft: (d) => { void gate.done.then(() => confirmDraft(d)); }, confirmRent: (r) => { void gate.done.then(() => confirmRent(r)); }, lang,
  };
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}
