"""Generate SYNTHETIC Bank-format statement PDFs (FAKE data only) so the pipeline can run
hermetically without any real bank data — now a PARAMETERIZED series generator.

Given a date range it emits ONE statement per period (monthly by default), with:
  * running balances that CARRY FORWARD (nouveau solde of month N == ancien solde of N+1),
  * a bounded RANDOM set of transactions per statement (a relevé is month-to-month, limited ops),
  * FAKE PII shapes in the libellés (contiguous 16-digit card PAN, `/BEN … /REF` beneficiary, a
    `FR..ZZZ..` creditor ICS) so the `build_silver` anonymize + residual gate has real work,
  * amounts >= 1000 (rendered with the French thousands SPACE `1 234,56`, which the extractor's
    fragment-joiner reassembles — a no-space `1234,56` would NOT parse; see the known-gaps note),
  * `ANCIEN SOLDE` / `NOUVEAU SOLDE` balance lines + a `TOTAL DES OPERATIONS` line,
  * multi-page PAGINATION (column titles reprinted on each page) when the ops overflow a page.

Every value is INVENTED and seeded (`--seed`) so a run is reproducible.

Usage:
    uv run --group build python landing/generate_synthetic.py                       # defaults below
    uv run --group build python landing/generate_synthetic.py --start 2025-01 --end 2026-01
    uv run --group build python landing/generate_synthetic.py --period-months 2 --seed 7 \
        --min-tx 8 --max-tx 22 --opening-balance 1500

Geometry mirrors what `functions/extract` keys on (parse geometry): date-comptable 48-100 · libellé
100-335 · date-valeur 335-400 · amount >= 400, with the debit/credit split at right-edge x=500 (debit
right edge < 500, credit >= 500). The SEMANTIC sense (matching `verify._classify`) is kept consistent
with the geometric column: credit libellés start `VIR RECU` / `REMBOURST`; debit start
`FACTURE(S) CARTE` / `RETRAIT` / `PRLV` / `VIR EMIS` / `COMMISSION`.

Output: `landing/synthetic-releve-<period>.pdf` — committable (matched by the .gitignore allow-rule).
"""

from __future__ import annotations

import argparse
import calendar
import random
import unicodedata
from datetime import date
from decimal import Decimal
from pathlib import Path

from faker import Faker
from reportlab.lib.pagesizes import A4
from reportlab.pdfgen import canvas

# Faker (fr_FR) is the fake-data source — realistic French names / companies / cities / IBAN / card PAN /
# creditor ICS — instead of hand-rolled pools. Seeded per run for reproducibility (see main()).
fake = Faker("fr_FR")

PAGE_W, PAGE_H = A4  # 595 × 842 pt

# Column geometry the extractor keys on (must not drift from functions/extract/handler.py).
X_DATE = 50       # date comptable (band 48-100)
X_LIB = 110       # libellé (band 100-335)
X_VAL = 340       # date valeur (band 335-400)
X_DEBIT_R = 490   # debit amount right edge (< 500 ⇒ DEBIT)
X_CREDIT_R = 560  # credit amount right edge (>= 500 ⇒ CREDIT)
FONT, SIZE = "Helvetica", 7
TOP_Y = PAGE_H - 60      # first header line
BOTTOM_Y = 70            # page break threshold
ROW_DY = 16              # vertical step per printed line

MONTHS_FR = ["", "JANVIER", "FEVRIER", "MARS", "AVRIL", "MAI", "JUIN", "JUILLET", "AOUT",
             "SEPTEMBRE", "OCTOBRE", "NOVEMBRE", "DECEMBRE"]

def _tok(s: str, maxlen: int = 22) -> str:
    """Bank-libellé token style: ASCII-fold (drop accents), uppercase, collapse spaces, cap length.
    Real machine libellés are uppercase ASCII; folding also keeps reportlab's Helvetica (Latin-1) safe."""
    ascii_s = unicodedata.normalize("NFKD", s).encode("ascii", "ignore").decode("ascii")
    return " ".join(ascii_s.upper().split())[:maxlen].strip()


def _fr(amount: Decimal) -> str:
    """French-formatted amount: space thousands separator, comma decimal (e.g. 1 234,56)."""
    return f"{amount:,.2f}".replace(",", " ").replace(".", ",")


def _pan() -> str:
    """A FAKE contiguous 16-digit card PAN (anonymizer masks it; residual gate flags a 16-digit run)."""
    return fake.numerify("4###############")  # 4 + 15 digits = 16 contiguous


def _ics() -> str:
    """A FAKE creditor id (ICS) matching the FR\\d{2}ZZZ… shape the anonymizer + residual set target."""
    return fake.numerify("FR##ZZZ######")


def _merchant() -> str:
    return _tok(fake.company())


def _city() -> str:
    return _tok(fake.city(), maxlen=16)


def _ben() -> str:
    return _tok(fake.name(), maxlen=20)


def _period_months(start: str, end: str, span: int) -> list[tuple[int, int]]:
    """List the (year, first-month) of each statement period from start..end (inclusive), stepping span."""
    sy, sm = (int(x) for x in start.split("-"))
    ey, em = (int(x) for x in end.split("-"))
    out: list[tuple[int, int]] = []
    y, m = sy, sm
    while (y, m) <= (ey, em):
        out.append((y, m))
        m += span
        while m > 12:
            m -= 12
            y += 1
    return out


def _period_dates(year: int, month: int, span: int) -> tuple[date, date, str, str]:
    """Return (first_day, last_day, header_label, filename_stem) for a `span`-month period."""
    last_y, last_m = year, month
    for _ in range(span - 1):
        last_m += 1
        if last_m > 12:
            last_m, last_y = 1, last_y + 1
    first = date(year, month, 1)
    last = date(last_y, last_m, calendar.monthrange(last_y, last_m)[1])
    if span == 1:
        label = f"{MONTHS_FR[month]} {year}"
        stem = f"{year}-{month:02d}"
    else:
        label = f"{MONTHS_FR[month]} {year} - {MONTHS_FR[last_m]} {last_y}"
        stem = f"{year}-{month:02d}_{last_y}-{last_m:02d}"
    return first, last, label, stem


def _rand_date(rng: random.Random, first: date, last: date) -> date:
    if first >= last:
        return first
    return date.fromordinal(rng.randint(first.toordinal(), last.toordinal()))


def _transactions(rng: random.Random, first: date, last: date, min_tx: int, max_tx: int) -> list[dict]:
    """Build a bounded, sorted set of FAKE transactions for the period. Always includes a salary credit
    (>= 1000, so the >=1000 path is exercised) + a rent + a utility PRLV; the rest are random one-offs."""
    txs: list[dict] = []

    def add(d: date, libelle: str, amount: Decimal, sens: str) -> None:
        txs.append({"date": d, "libelle": libelle[:52], "amount": amount, "sens": sens})

    # recurring, consistent month-to-month
    add(_rand_date(rng, first, first.replace(day=min(5, last.day))),
        f"VIR RECU /BEN {_ben()} /REF {rng.randint(10, 99)} SALAIRE",
        Decimal(f"{rng.randint(1800, 3200)}.{rng.randint(0, 99):02d}"), "credit")
    add(_rand_date(rng, first, last),
        f"PRLV SEPA ID EMETTEUR/{_ics()} LOYER",
        Decimal(f"{rng.randint(600, 1100)}.{rng.randint(0, 99):02d}"), "debit")
    add(_rand_date(rng, first, last),
        f"PRLV SEPA ID EMETTEUR/{_ics()} {_merchant()}",
        Decimal(f"{rng.randint(20, 120)}.{rng.randint(0, 99):02d}"), "debit")

    # random one-offs up to the cap
    n_more = max(0, rng.randint(min_tx, max_tx) - len(txs))
    for _ in range(n_more):
        kind = rng.choices(["card", "retrait", "vir_emis", "refund"], weights=[6, 2, 1, 1])[0]
        if kind == "card":
            add(_rand_date(rng, first, last),
                f"FACTURE(S) CARTE {_pan()} {_merchant()}",
                Decimal(f"{rng.randint(5, 90)}.{rng.randint(0, 99):02d}"), "debit")
        elif kind == "retrait":
            d = _rand_date(rng, first, last)
            add(d, f"RETRAIT DAB {d.strftime('%d.%m')} {_city()}",
                Decimal(f"{rng.randint(20, 200)}.00"), "debit")
        elif kind == "vir_emis":
            add(_rand_date(rng, first, last),
                f"VIR EMIS /BEN {_ben()} /REF {rng.randint(10, 99)} PRET",
                Decimal(f"{rng.randint(50, 400)}.{rng.randint(0, 99):02d}"), "debit")
        else:
            add(_rand_date(rng, first, last),
                f"REMBOURST {_merchant()}",
                Decimal(f"{rng.randint(10, 80)}.{rng.randint(0, 99):02d}"), "credit")

    txs.sort(key=lambda t: t["date"])
    return txs


def _draw_titles(c: canvas.Canvas, y: float) -> float:
    """Draw the column-title line (skipped by the extractor — no date in the date column)."""
    c.drawString(X_DATE, y, "DATE")
    c.drawString(X_LIB, y, "NATURE DES OPERATIONS")
    c.drawString(X_VAL, y, "VALEUR")
    c.drawRightString(X_DEBIT_R, y, "DEBIT")
    c.drawRightString(X_CREDIT_R, y, "CREDIT")
    return y - ROW_DY


def _render(path: Path, holder: str, iban: str, label: str,
            ancien: Decimal, txs: list[dict], nouveau: Decimal,
            tot_deb: Decimal, tot_cre: Decimal) -> None:
    # invariant=1 → reportlab omits the wall-clock timestamp/doc-id, so identical (seed, args) produce
    # byte-identical PDFs (no spurious diffs when the committed fixture is regenerated).
    c = canvas.Canvas(str(path), pagesize=A4, invariant=1)
    c.setFont(FONT, SIZE)

    def new_page_titles(first_page: bool) -> float:
        if not first_page:
            c.showPage()
            c.setFont(FONT, SIZE)
        y = TOP_Y
        for line in (f"RELEVE DE COMPTE {'' if first_page else '(SUITE)'}",
                     f"Titulaire : {holder}", f"IBAN : {iban}", f"PERIODE : {label}"):
            c.drawString(X_DATE, y, line)
            y -= ROW_DY
        y -= 6
        return _draw_titles(c, y)

    y = new_page_titles(first_page=True)
    # ANCIEN SOLDE (no date ⇒ not a transaction; verify SKIPs "Solde")
    c.drawString(X_LIB, y, "ANCIEN SOLDE")
    c.drawRightString(X_CREDIT_R, y, _fr(ancien))
    y -= ROW_DY + 4

    for t in txs:
        if y < BOTTOM_Y:
            y = new_page_titles(first_page=False)
        c.drawString(X_DATE, y, t["date"].strftime("%d.%m.%y"))
        c.drawString(X_LIB, y, t["libelle"])
        c.drawString(X_VAL, y, t["date"].strftime("%d.%m.%y"))
        c.drawRightString(X_DEBIT_R if t["sens"] == "debit" else X_CREDIT_R, y, _fr(t["amount"]))
        y -= ROW_DY

    if y < BOTTOM_Y + 3 * ROW_DY:
        y = new_page_titles(first_page=False)
    y -= 6
    c.drawString(X_LIB, y, "TOTAL DES OPERATIONS")
    c.drawRightString(X_DEBIT_R, y, _fr(tot_deb))
    c.drawRightString(X_CREDIT_R, y, _fr(tot_cre))
    y -= ROW_DY + 4
    c.drawString(X_LIB, y, "NOUVEAU SOLDE")
    c.drawRightString(X_CREDIT_R, y, _fr(nouveau))
    c.showPage()
    c.save()


def main() -> None:
    ap = argparse.ArgumentParser(description="Generate a FAKE Bank statement series (see docstring).")
    ap.add_argument("--start", default="2025-01", help="first period YYYY-MM (default 2025-01)")
    ap.add_argument("--end", default="2025-11", help="last period YYYY-MM, inclusive (default 2025-11)")
    ap.add_argument("--period-months", type=int, default=1, help="months per statement (1=monthly, 2=bi)")
    ap.add_argument("--seed", type=int, default=0, help="RNG seed (reproducible)")
    ap.add_argument("--min-tx", type=int, default=8, help="min transactions per statement")
    ap.add_argument("--max-tx", type=int, default=22, help="max transactions per statement")
    ap.add_argument("--opening-balance", type=str, default="1500.00", help="first statement's ancien solde")
    ap.add_argument("--holder", default="COMPTE DEMO", help="FAKE account holder name")
    ap.add_argument("--out-dir", default=str(Path(__file__).resolve().parent),
                    help="output dir (default: landing/)")
    args = ap.parse_args()

    rng = random.Random(args.seed)   # numeric control (counts, amounts, dates)
    Faker.seed(args.seed)            # fake text data (names/companies/cities/PAN/ICS/IBAN) — reproducible
    iban = fake.iban()
    balance = Decimal(args.opening_balance)
    out_dir = Path(args.out_dir)
    out_dir.mkdir(parents=True, exist_ok=True)

    written = 0
    for (year, month) in _period_months(args.start, args.end, args.period_months):
        first, last, label, stem = _period_dates(year, month, args.period_months)
        txs = _transactions(rng, first, last, args.min_tx, args.max_tx)
        tot_deb = sum((t["amount"] for t in txs if t["sens"] == "debit"), Decimal("0"))
        tot_cre = sum((t["amount"] for t in txs if t["sens"] == "credit"), Decimal("0"))
        ancien = balance
        nouveau = ancien + tot_cre - tot_deb
        path = out_dir / f"synthetic-releve-{stem}.pdf"
        _render(path, args.holder, iban, label, ancien, txs, nouveau, tot_deb, tot_cre)
        print(f"wrote {path.name}  ({len(txs)} tx | debit={_fr(tot_deb)} credit={_fr(tot_cre)} "
              f"| ancien={_fr(ancien)} nouveau={_fr(nouveau)})")
        balance = nouveau
        written += 1
    print(f"— {written} statement(s), seed={args.seed}, period={args.period_months}mo")


if __name__ == "__main__":
    main()
