# Bank Statement Import Runbook

## What it does

Bank statement import reads a statement file and creates reviewable suggestions.
Nothing is posted to the ledger until the user confirms each suggestion. The
flow uses no bank credentials, provider or consent flow.

## Supported source

The importer accepts Fibank statement XML in the `SYTY_BAL_CUST_MAIN` format,
exported from My Fibank. Fibank XLS is not supported; CSV and XLSX decoders are
future work.

The parser skips opening and closing balance rows and turnover-total rows
(`TRN_TYPE` `OP`, `CL`, `DT` or `CT`). It ignores customer and other PII blocks
(`ROWSET`).

Row handling follows this matrix:

| Outcome | Row rules |
|---------|-----------|
| **Skipped** (not failures; increments `skipped_rows`) | `TRN_TYPE` is `OP`, `CL`, `DT` or `CT`; `DTKT` is empty; or the parsed amount is zero. |
| **Failure** (reported, non-fatal) | Bad direction (a non-empty `DTKT` other than `D` or `C`), bad amount (unparseable or negative), bad booked/value dates, or an invalid closing-balance row. The row is rejected, recorded in `failures` with its 0-based raw-row index and a safe, value-free reason. |
| **Imported** | Everything else. Unknown transaction types and missing counterparties are imported; the merchant may show as `Unknown` in the suggestion. An unparseable running `AC_BAL` does not reject the row: it imports without a running balance. |
| **Fatal structural problem** | Malformed XML, doctype/entity declarations, excessive nesting, a wrong or missing account block, an invalid document-level balance or period, or more than 20000 raw rows. These return `400` with the reason in the message. |

In short: skipped = `OP|CL|DT|CT`, empty `DTKT` or zero amount; failure = bad
direction, bad amount, bad dates or an invalid closing-balance row; everything
else imports unless a fatal structural problem invalidates the statement.

## Import a statement

1. Open **Household settings → Import bank statement**.
2. Choose the local account that matches the statement IBAN. The account is
   selected manually; IBANs are not stored on accounts.
3. Upload the statement `.xml` file from the local device.
4. Inspect the preview: bank, IBAN, period, currency, opening and closing
   balances, entry count and skipped-row count.
5. Select **Import** to create suggestions for review.

## Preview fields

Each parsed entry shows its booked date and value date, direction, amount,
merchant, description, reference, transaction code and running balance.

The preview also returns a `failures` list. `row_index` is the 0-based index in
the raw statement row sequence (skipped rows included), so parser failures and
validation failures (currency mismatch, bad date, non-positive amount) share
one numbering and can be matched against the source file. `AC_ENTRY_SR_NO` is
not required: rows without it import using a content-based fallback
fingerprint.

## Commit and deduplication

Each import creates one batch. Every row is fingerprinted within its household
and account. When `AC_ENTRY_SR_NO` is present, the fingerprint uses
`<bank>|<AC_ENTRY_SR_NO>`.

Re-uploading the same file imports zero rows and reports the duplicates.
Suggestions that were cancelled are not recreated by a later import.

## Review inbox

Imported suggestions show their date, account and direction in the review inbox.
You can edit a suggestion before confirming it.

- **DEBIT:** confirm as a purchase, choosing the purchase kind, account and
  envelope.
- **CREDIT:** confirm as income, choosing the account and effective date only.
  It posts `OP_INCOME`.

An internal transfer between your own accounts appears as a debit on one
statement and a credit on the other. Importing and confirming both sides
double-counts the transfer. Confirm only the side that represents real movement,
or wait for future transfer matching.

## Limits and security

- The import handler has a 10 MiB file limit.
- The global `HTTP_MAX_BODY_BYTES` limit defaults to 1 MiB and allows at most
  16 MiB. Raise it for a large export when needed; the handler's 10 MiB limit
  still applies.
- Files are parsed in memory and are not retained. Only normalized rows and
  import batches are stored.
- Non-UTF-8 exports are handled: the parser decodes `windows-1251`,
  `windows-1252` and `iso-8859-1`/`latin1` declarations to UTF-8. Any other
  declared charset fails with a named reason instead of a generic parse error.
- Logs do not contain statement contents or customer data.
- Rows whose currency differs from the household currency fail with a per-row
  reason. One bad row never aborts a statement: the response `failures` list
  names the rejected raw rows and valid rows still import. Batch `rows_total`
  counts parsed entries plus parser-rejected rows, and `rows_failed` counts
  both kinds of failure.

## Troubleshooting

| Symptom | Check |
|---------|-------|
| `400` statement error | The response message names the cause, e.g. `The statement file is invalid: no account block found.` — read the reason before re-exporting. Reasons are safe and never quote file contents. |
| `400` unknown format | The file was not recognized as Fibank `SYTY_BAL_CUST_MAIN` XML. Confirm it is XML, not XLS, CSV or XLSX. |
| Some rows missing after import | Check the `failures` list in the preview or commit response: each rejected raw row is reported with its index and a safe reason. A single bad row does not block the rest of the statement. |
| `413` payload too large | Check the global `HTTP_MAX_BODY_BYTES` setting and the 10 MiB handler limit. |
| Zero entries | Check that the file is the expected statement export and contains transaction rows, not only opening/closing balances or totals. |
| Currency mismatch | Check the household currency and the currency of each failed row. |
| Duplicates on re-import | This is expected when the same statement rows were already imported. Check the batch and entry counts before retrying. |

For API and persisted state, check:

```sh
docker compose logs api
```

Use `GET /households/{id}/bank-imports` to inspect batches. Check row counts in
`bank_import_entries` when diagnosing persisted import details.

## Verification queries

Run these against the application database:

```sql
select bank_code, file_name, rows_total, rows_imported, rows_duplicate, rows_failed
from bank_import_batches
order by created_at desc;

select direction, count(*)
from suggestions
where import_batch_id is not null
group by direction;
```
