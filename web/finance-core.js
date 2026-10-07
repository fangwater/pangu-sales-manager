/* Report arithmetic also used by the export; no extrapolation of unpriced lines. */
(function (root) {
  const numericFields = [
    "units", "refund_total", "refund_orders", "sales_chargebacks", "freight_chargebacks", "buyer_chargebacks",
    "payback_amount", "sales_receipt_amount", "freight_receipt_amount", "sales_chargeback_amount", "freight_chargeback_amount",
    "fulfillment_delay_amount", "fulfillment_false_ship_amount", "buyer_chargeback_amount", "shipping_label_fee_amount",
    "return_label_fee_merchant_amount", "return_label_fee_third_party_amount", "platform_return_label_fee_amount",
    "estimated_sales_amount", "estimated_sales_matched", "estimated_sales_total", "settled_rows", "fee_rows",
    "known_fee_balance_amount", "disposal_fee_amount", "platform_balance_amount",
  ];
  const unknownFields = ["tax_withheld_amount", "tax_refund_amount", "non_order_transaction_fee_amount"];
  function sumRows(rows) {
    const result = Object.fromEntries(numericFields.map(key => [key, 0]));
    unknownFields.forEach(key => { result[key] = null; });
    rows.forEach(row => {
      numericFields.forEach(key => { result[key] += Number(row[key] || 0); });
      unknownFields.forEach(key => { if (row[key] != null) result[key] = (result[key] || 0) + Number(row[key]); });
    });
    return result;
  }
  function bucketKey(date, period) {
    if (period === "month") return date.slice(0, 7) + "-01";
    if (period !== "week") return date;
    const d = new Date(date + "T00:00:00Z");
    d.setUTCDate(d.getUTCDate() - (d.getUTCDay() + 6) % 7);
    return d.toISOString().slice(0, 10);
  }
  function groupDaily(rows, period, start, end) {
    const groups = new Map();
    rows.filter(row => (!start || row.bucket >= start) && (!end || row.bucket <= end)).forEach(row => {
      const key = bucketKey(row.bucket, period);
      if (!groups.has(key)) groups.set(key, []);
      groups.get(key).push(row);
    });
    return [...groups.entries()].sort(([a], [b]) => a.localeCompare(b)).map(([key, values]) => ({
      ...sumRows(values), bucket: key, label: period === "month" ? key.slice(0, 7) : period === "week" ? key + " 周" : key,
      first_date: values[0].bucket, last_date: values[values.length - 1].bucket,
    }));
  }
  function coverage(row) { return Number(row.estimated_sales_total) > 0 ? Number(row.estimated_sales_matched) / Number(row.estimated_sales_total) * 100 : null; }
  function hasLedger(row) { return Number(row.settled_rows || 0) + Number(row.fee_rows || 0) > 0; }
  function csvCell(value) {
    let text = String(value ?? "");
    if (typeof value === "string" && /^[\s]*[=+\-@]/.test(text)) text = "'" + text;
    return /[",\r\n]/.test(text) ? '"' + text.replaceAll('"', '""') + '"' : text;
  }
  const api = { sumRows, bucketKey, groupDaily, coverage, hasLedger, csvCell };
  root.FinanceCore = api;
  if (typeof module !== "undefined") module.exports = api;
})(typeof window !== "undefined" ? window : globalThis);
