"use client";
import { useT } from "@/components/I18n";
import { useEffect, useState } from "react";
import { fmt } from "@/lib/format";
import { PRICES } from "@/lib/demo";
import { api, type PriceItem } from "@/lib/api";
import { useStore } from "../Store";
import { Screen } from "./common";

const at = (p: PriceItem, market: string) => p.markets.find(m => m.market === market && m.source !== "narx_stat")?.price;
/** Official weekly average: Tashkent city when published, otherwise the republic. */
const official = (p: PriceItem) => (p.markets.find(m => m.source === "narx_stat" && m.market === "toshkent_city") || p.markets.find(m => m.source === "narx_stat"))?.price;
/** The lowest bazaar median (anonymous users or sample): what the same goods cost elsewhere. */
const bazaar = (p: PriceItem) => { const v = p.markets.filter(m => m.source !== "narx_stat").map(m => m.price); return v.length ? Math.min(...v) : undefined; };

const DEMO: PriceItem[] = PRICES.map(p => ({ product: p.name, name: p.name, unit: "kg", mine: { price: p.mine, at: "" },
  markets: [{ market: "chorsu", price: p.chorsu, source: "seed", observed_at: "", sample: true }, { market: "oloy", price: p.oloy, source: "seed", observed_at: "", sample: true }] }));

export default function Bozor() { const t = useT();
  const s = useStore();
  const [data, setData] = useState<{ items: PriceItem[]; sample: boolean } | null>(null), [err, setErr] = useState(false);
  useEffect(() => {
    if (!s.remote) return;
    let on = true;
    api.prices().then(r => { if (on) setData(r); }).catch(() => { if (on) setErr(true); });
    return () => { on = false; };
  }, [s.remote]);
  const items = s.remote && data ? data.items : DEMO;
  const sample = !s.remote || err || !data || data.sample;
  // The product where the user pays the most above the lowest bazaar median.
  const worst = items.map(p => ({ p, base: bazaar(p) })).filter((x): x is { p: PriceItem; base: number } => !!x.p.mine && !!x.base && x.p.mine.price > x.base)
    .sort((a, b) => (b.p.mine!.price - b.base) / b.base - (a.p.mine!.price - a.base) / a.base)[0];
  const diff = worst ? worst.p.mine!.price - worst.base : 0, pct = worst ? Math.round((diff / worst.base) * 100) : 0;
  return <Screen id="bozor" title={t("Bozor narxlari")}><div className="g2">
    <div className="col">
      {worst && <div className="blk"><div className="big1"><p className="l">{t("Bozor medianasidan qimmatroq toʻladingiz")}</p><p className="n num">+{fmt(diff)} <small>{t("soʻm / kg")}</small></p>
        <p className="sub1">{t("{n} narxingiz bozor medianasidan {p}% qimmat", { n: t(worst.p.name), p: pct })}</p></div></div>}
      <div className="blk"><p className="bh">{t("1 kg narxi")}</p><div className="list" style={{ paddingTop: 6, paddingBottom: 6 }}>
        {items.length === 0 ? <p className="note">{t("Narx kuzatuvlari hali yoʻq")}</p> : <table className="t">
          <thead><tr><th>{t("Mahsulot")}</th><th>{t("Siz")}</th><th>{t("Chorsu")}</th><th>{t("Oloy")}</th><th>{t("Rasmiy")}</th></tr></thead>
          <tbody>{items.map(p => { const base = bazaar(p), mine = p.mine?.price;
            return <tr key={p.product}><td>{t(p.name)}</td><td className={mine && base && mine > base * 1.03 ? "up" : ""}>{mine ? fmt(mine) : "—"}</td>
              <td>{at(p, "chorsu") ? fmt(at(p, "chorsu")!) : "—"}</td><td>{at(p, "oloy") ? fmt(at(p, "oloy")!) : "—"}</td><td>{official(p) ? fmt(official(p)!) : "—"}</td></tr>; })}</tbody></table>}
      </div></div>
    </div>
    <div className="col">
      <div className="blk"><p className="note">{sample ? t("Namuna maʼlumotlar · narx.stat.uz va foydalanuvchilar xaridlari") : t("Manba: Milliy statistika qoʻmitasi (narx.stat.uz) va Consul foydalanuvchilarining anonim xaridlari")}</p></div>
    </div>
  </div></Screen>;
}
