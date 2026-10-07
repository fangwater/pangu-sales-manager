const { test } = require("node:test");
const assert = require("node:assert/strict");
const core = require("../web/finance-core.js");

test("filter dates before bucketing so partial weeks do not include hidden days", () => {
  const rows = [{ bucket: "2026-08-30", units: 100, settled_rows: 1 }, { bucket: "2026-08-31", units: 2, settled_rows: 1, payback_amount: 20 }, { bucket: "2026-09-01", units: 3, settled_rows: 0 }, { bucket: "2026-09-02", units: 500 }];
  const grouped = core.groupDaily(rows, "week", "2026-08-31", "2026-09-01");
  assert.equal(grouped.length, 1); assert.equal(grouped[0].units, 5); assert.equal(grouped[0].payback_amount, 20);
  assert.equal(grouped[0].bucket, "2026-08-31"); assert.equal(grouped[0].tax_withheld_amount, null);
});
test("aggregate coverage by line counts rather than averaging percentages", () => {
  const total = core.sumRows([{ estimated_sales_matched: 1, estimated_sales_total: 1, estimated_sales_amount: 10 }, { estimated_sales_matched: 0, estimated_sales_total: 99 }]);
  assert.equal(core.coverage(total), 1); assert.equal(total.estimated_sales_amount, 10);
  assert.equal(core.coverage({ estimated_sales_total: 0 }), null);
});
test("distinguish an actual zero balance from no imported financial records", () => {
  assert.equal(core.hasLedger({ payback_amount: 0, settled_rows: 1 }), true);
  assert.equal(core.hasLedger({ payback_amount: 0 }), false);
});
test("CSV preserves numeric debits and prevents spreadsheet formula execution", () => {
  assert.equal(core.csvCell(-10), "-10"); assert.equal(core.csvCell("=HYPERLINK(\"x\")"), '"\'=HYPERLINK(""x"")"');
  assert.equal(core.csvCell("a,b\nc"), '"a,b\nc"');
});
