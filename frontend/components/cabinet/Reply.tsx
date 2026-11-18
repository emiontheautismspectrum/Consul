"use client";
import { Link, useT } from "@/components/I18n";
import { Fragment, useState } from "react";
import type { Action, Reply } from "@/lib/answer";
import { Mark } from "@/components/Icons";
import { useStore } from "./Store";
import { BankReport, PlanReport, PlanWizard, WhySheet } from "./Sheets";
import { AddOperation } from "./AddOperation";
import type { ApiReply } from "@/lib/api";

/** Renders text with **bold** markers. */
export function Rich({ text }: { text: string }) {
  return <>{text.split(/\*\*(.+?)\*\*/g).map((p, i) => (i % 2 ? <b key={i}>{p}</b> : <Fragment key={i}>{p}</Fragment>))}</>;
}

export function ActionLink({ a, r }: { a: Action; r?: Reply }) {
  const s = useStore();
  // Server replies carry the draft the assistant prepared; it is recorded only on this tap.
  const server = r as ApiReply | undefined;
  const cls = a.primary ? "pri" : "lk";
  if (a.href) return <Link className={cls} href={a.href} onClick={() => { s.closeSheet(); if (a.cx) s.setCalcTab(a.cx); }}>{a.label}</Link>;
  const run = () => {
    if (a.run === "why") s.openSheet(<WhySheet />);
    if (a.run === "bankReport") s.openSheet(<BankReport />);
    if (a.run === "planWizard") s.openSheet(<PlanWizard fresh />);
    if (a.run === "planReport") s.openSheet(<PlanReport />);
    const cmd = a.run as string | undefined;
    if (cmd === "confirm_op" && server?.pending_op) { s.closeSheet(); s.confirmDraft(server.pending_op); }
    if (cmd === "edit_op") s.openSheet(<AddOperation />);
    if (cmd === "add_rent" && server?.rent) { s.closeSheet(); s.confirmRent(server.rent); }
  };
  return <a className={cls} onClick={run} role="button" tabIndex={0}>{a.label}</a>;
}

function Speaking() { const t = useT();
  return <div className="spk"><span className="wv sm">{Array.from({ length: 8 }, (_, i) => <i key={i} />)}</span>{t("Javob ovozda oʻqilmoqda")}</div>;
}

export function Bubble({ r, voice }: { r: Reply; voice?: boolean }) { const t = useT();
  const [open, setOpen] = useState(false);
  return (
    <div className="mc"><svg className="av2" aria-hidden="true"><use href="#sq" /></svg><div className="bd">
      {voice && <Speaking />}
      <p className="tx"><Rich text={r.text} /></p>
      {r.card && <div className="acard"><p className="t1">{r.card.title}</p><p className="t2">{r.card.value}</p>{r.card.rows.map(([a, b]) => <div className="r" key={a}><span>{a}</span><b>{b}</b></div>)}</div>}
      {r.actions && <div className="acts">{r.actions.map(a => <ActionLink a={a} r={r} key={a.label} />)}</div>}
      {r.ask && <p className="tx">{r.ask}</p>}
      {r.work && r.work.length > 0 && <>
        <button className="how" onClick={() => setOpen(o => !o)}>{t("Qanday hisoblandi?")}</button>
        <ul className={"work" + (open ? " open" : "")}>{r.work.map(([ok, l]) => <li key={l}>{ok ? "✓" : "!"} {l}</li>)}</ul>
      </>}
    </div></div>
  );
}

export function Thinking() { const t = useT();
  return <div className="mc"><Mark className="av2" /><div className="bd"><span className="think"><Mark size={18} />{t("Hisoblayapman…")}</span></div></div>;
}

export function AnswerSheet({ q, r }: { q: string; r: Reply }) { const t = useT();
  const s = useStore();
  return <>
    <p className="cgrp" style={{ margin: "0 0 6px" }}>{t("Siz: «")}{q}»</p>
    <Bubble r={r} />
    <Link className="btn sec w" style={{ marginTop: 12 }} href="/app/consul" onClick={() => { s.closeSheet(); setTimeout(() => s.chat(q), 250); }}>{t("Consul bilan davom ettirish")}</Link>
  </>;
}
