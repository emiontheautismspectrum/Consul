"use client";
import { Link, useT, tk, useMln } from "@/components/I18n";
import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import { fmt, formatInput } from "@/lib/format";
import { safeLoan } from "@/lib/finance";
import { TURNOVER_1PCT_LIMIT } from "@/lib/tax";
import { useStore } from "../Store";
import { Screen } from "./common";

const INVITE = tk("Assalomu alaykum! Men Consul ilovasida savdoni yozib boraman. YaTT sifatida roʻyxatdan oʻtsangiz, soliq aylanmadan 1%, bank krediti va davlat yordamiga yoʻl ochiladi. Consul hisoblab beradi.");

const WHY: [string, string][] = [["fear_tax", tk("Soliq va tekshiruvdan qoʻrqadi")], ["complex_docs", tk("Hujjatlar murakkab")], ["no_benefit", tk("Foydasini koʻrmaydi")], ["other", tk("Boshqa")]];

/** One question for the Chamber report ("why don't you register?"), shown only to unregistered traders. */
function WhySurvey() { const t = useT();
  const s = useStore();
  const [show, setShow] = useState(false), [done, setDone] = useState(false);
  useEffect(() => { if (s.remote) api.me().then(me => setShow(me.business?.status === "none")).catch(() => {}); }, [s.remote]);
  if (!show) return null;
  return <div className="blk"><p className="bh">{t("Nega roʻyxatdan oʻtmaysiz?")}</p>
    {done ? <p className="note">{t("Rahmat! Javobingiz ismsiz, umumiy raqamlar ichida.")}</p>
      : <div className="list">{WHY.map(([k, label]) => <a className="li" key={k} role="button" tabIndex={0} onClick={() => { api.survey(k).then(() => setDone(true)).catch(() => {}); }}><span>{t(label)}</span><i className="chev">›</i></a>)}</div>}
  </div>;
}

export default function Oq() { const t = useT(), mln = useMln();
  const s = useStore();
  const [turn, setTurn] = useState("30 000 000"), [m, setM] = useState(0.12);
  const T = +turn.replace(/\D/g, "") || 0, y = T * 12, r = y <= TURNOVER_1PCT_LIMIT ? 0.01 : 0.04, pr = T * m, L = safeLoan(pr);
  const rows: [React.ReactNode, string][] = [
    [<>{t("Soliq")}<small>{t("aylanmadan")} {r * 100}%{y > TURNOVER_1PCT_LIMIT ? t(" · yiliga 1 mlrd dan oshadi") : ""}</small></>, fmt(T * r) + t(" / oy")],
    [<>{t("Taxminiy foyda")}<small>{t("Consulda shu soha oʻrtachasi")} {Math.round(m * 100)}%</small></>, fmt(pr) + t(" / oy")],
    [<>{t("Xavfsiz kredit")}<small>{t("toʻlov foydaning 30% i · 24 oy")}</small></>, L ? t("{x} gacha", { x: mln(L) }) : "—"],
    [<>{t("Garovsiz mikrokredit")}<small>{t("ijobiy kredit tarixi bilan — 200 mln gacha")}</small></>, t("100 mln gacha")],
  ];
  const share = () => navigator.clipboard?.writeText(t(INVITE)).then(() => s.showToast({ main: t("Nusxalandi"), sub: t("SMS yoki messenjerga joylashtiring") }), () => s.openSheet(<><h4>{t("Taklif matni")}</h4><textarea className="ta" defaultValue={t(INVITE)} /></>));
  return <Screen id="oq" title={t("Oq biznes")}><div className="g2">
    <div className="col">
      <WhySurvey />
      <div className="blk"><div className="big1"><p className="l">{t("Sizning maqomingiz")}</p><p className="n" style={{ whiteSpace: "normal", fontSize: 28 }}>{t("Rasmiy · YaTT ✓")}</p><p className="sub1">{t("aylanmadan 1% · yiliga 1 mlrd gacha")}</p></div></div>
      <div className="blk"><p className="bh">{t("Bu sizga nima beradi")}</p><div className="list">
        <Link className="li" href="/app/kredit"><span>{t("Bank krediti")}<small>{t("daromad tarixi bilan · bank qarori")}</small></span><span className="lr"><b>{t("34 mln gacha")}</b><i className="chev">›</i></span></Link>
        <Link className="li" href="/app/kredit"><span>{t("Davlat yordami")}<small>{t("foiz kompensatsiyasi, kafillik")}</small></span><i className="chev">›</i></Link>
        <div className="li"><span>{t("Karta va QR orqali toʻlov")}<small>{t("naqdsiz mijozlarni yoʻqotmaysiz")}</small></span><b className="tk">✓</b></div>
        <div className="li"><span>{t("Tekshiruvdan xotirjam")}<small>{t("hisob tartibda, Consulda")}</small></span><b className="tk">✓</b></div>
      </div></div>
    </div>
    <div className="col">
      <div className="blk"><p className="bh">{t("Tanishingiz roʻyxatdan oʻtsa")}</p>
        <div className="oqf"><label><span>{t("Oylik aylanmasi")}</span><input inputMode="numeric" value={turn} onChange={e => setTurn(formatInput(e.target.value))} /></label>
          <div className="seg">{[[0.12, t("Savdo")], [0.25, t("Xizmat")]].map(([k, l]) => <button key={l} className={m === k ? "on" : ""} onClick={() => setM(k as number)}>{l}</button>)}</div></div>
        <div className="list">{rows.map(([a, b], i) => <div className="li" key={i}><span>{a}</span><b>{b}</b></div>)}</div></div>
      <div className="blk"><p className="bh">{t("Qadamlar")}</p><div className="list" style={{ padding: "14px 16px" }}><ol className="steps">
        <li>{t("my.gov.uz yoki Davlat xizmatlari markazida YaTT sifatida roʻyxatdan oʻtish")}</li><li>{t("Soliq rejimi: aylanmadan")}</li><li>{t("Toʻlovlar uchun QR-kod olish")}</li><li>{t("Consulda savdoni yozib borish")}</li></ol></div></div>
      <button className="btn sec w" onClick={share}>{t("Taklif matnini nusxalash")}</button>
      <p className="note">{t("Consul shaxsiy maʼlumotlaringizni davlatga yubormaydi. Palataga faqat anonim umumiy raqamlar — roziligingiz bilan.")}</p>
    </div>
  </div></Screen>;
}
