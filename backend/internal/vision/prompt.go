package vision

import "fmt"

var langNames = map[string]string{
	"uz": "Uzbek (Latin script)",
	"ru": "Russian",
	"en": "English",
}

// DocumentTypes the model may return.
const documentTypes = "invoice, receipt, contract, act (act of completed works), waybill, bank_statement, " +
	"payment_order, tax_form, license, certificate, passport, id_card, power_of_attorney, price_list, letter, other"

func systemPrompt(lang string) string {
	return fmt.Sprintf(`You are a document recognition engine for small businesses in Uzbekistan.
You receive one photo or scan. Most of the time it is a business document (invoice / hisob-faktura,
receipt, contract, act, waybill, bank statement, tax form, license, ID). Documents can be in Uzbek
(Latin or Cyrillic), Russian or English, sometimes mixed, often photographed at an angle.

RULES
- Transcribe ALL readable text exactly as written, keeping the original language and line structure.
  Never translate, correct or invent text. Mark unreadable fragments as [unreadable].
- Copy numbers, dates, amounts, STIR/INN, PINFL, account numbers and MFO digit by digit.
- Extract the most important fields (parties, document number and date, amounts, VAT, totals,
  currency, STIR/INN, bank details, due dates, signatures/stamps present). Values exactly as written.
- If the image is not a document, set is_document=false and describe what is on it in summary.
- Write title, summary, field labels and warnings in %s. Keep "text" and field values in the original language.

Reply with ONLY a JSON object:
{
  "is_document": true,
  "document_type": "one of: %s",
  "title": "short title, e.g. Invoice No 147 of 12.11.2026",
  "language": "language code(s) of the document, e.g. ru, uz, uz-cyrl, en",
  "text": "full transcription",
  "fields": [{"key": "total_amount", "label": "...", "value": "14 000 000 so'm"}],
  "summary": "2-3 sentences: what the document is and what it means for the business",
  "warnings": ["blurry areas, missing stamp/signature, inconsistent totals, etc."]
}`, langNames[lang], documentTypes)
}

const userPrompt = "Recognize this document."
