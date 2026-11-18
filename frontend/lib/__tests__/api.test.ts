import { describe, expect, it } from "vitest";
import { dayLabel, opTitle, toLedger, type ApiDebtor, type ApiOp, type ApiToday } from "../api";
import { view } from "../ledger";
import today from "./fixtures/demo-today.json";
import ops from "./fixtures/demo-ops.json";
import debtors from "./fixtures/demo-debtors.json";

// Real /ledger/today, /ops and /debtors responses for the demo account (Dilnoza).
describe("server state → cabinet ledger", () => {
  const l = toLedger(today as ApiToday, ops as ApiOp[], debtors as ApiDebtor[], []);
  const v = view(l), s = (today as ApiToday).view;

  it("reproduces the server's numbers through the frontend view()", () => {
    expect(v.free).toBe(s.free);
    expect(v.obligations).toBe(s.obligations);
    expect(v.tax).toBe(s.tax);
    expect(v.cash).toBe(s.cash);
    expect(v.profit).toBe(s.profit);
    expect(v.margin).toBe(s.margin);
    expect(v.owedToMe).toBe(s.owed_to_me);
    expect(v.debtorCount).toBe(s.debtor_count);
  });

  it("gives the plan's reference numbers for the demo account", () => {
    expect(v.free).toBe(4_200_000);
    expect(v.obligations).toBe(6_600_000);
    expect(l.debtors.map(d => d.name)).toEqual(["Bekzod", "Karim", "Shoira opa"]);
  });

  it("titles ops like apply() does", () => {
    const op = (kind: ApiOp["kind"], extra: Partial<ApiOp> = {}): ApiOp =>
      ({ id: "1", kind, amount: 100, method: "naqd", label: "", counterparty: null, fiscal: false, source: "text", occurred_at: new Date().toISOString(), ...extra });
    expect(opTitle(op("sale"))).toBe("Sotuv");
    expect(opTitle(op("exp", { label: "ijara" }))).toBe("Ijara");
    expect(opTitle(op("nas", { counterparty: "Bekzod" }))).toBe("Nasiya · Bekzod");
    expect(opTitle(op("repaid", { counterparty: "Karim" }))).toBe("Nasiya qaytdi · Karim");
  });

  it("labels days in Tashkent time", () => {
    const now = new Date("2026-11-18T07:00:00Z");
    expect(dayLabel("2026-11-18T03:00:00Z", now)).toBe("Bugun");
    expect(dayLabel("2026-11-17T10:00:00Z", now)).toBe("Kecha");
    expect(dayLabel("2026-11-14T20:00:00Z", now)).toBe("15-noyabr"); // 01:00 on the 15th in Tashkent
  });
});
