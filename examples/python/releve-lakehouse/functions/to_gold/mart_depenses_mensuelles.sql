-- mart_depenses_mensuelles — monthly debit/credit/balance rollup, the gold serving table (ADR-0086).
-- Runs INSIDE the `lake` catalog over Quack: it reads the conformed silver Parquet (the catalog's own
-- `silver` binding) and materializes a DuckLake table under gold/. One snapshot per run (DuckLake
-- versioning), so the dashboard reads a small aggregated table, not the raw transactions.
CREATE OR REPLACE TABLE mart_depenses_mensuelles AS
SELECT
    date_trunc('month', date_comptable)      AS mois,
    count(*)                                 AS nb_operations,
    sum(debit)                               AS total_debit,
    sum(credit)                              AS total_credit,
    sum(credit) - sum(debit)                 AS solde_net
FROM read_parquet('s3://releves/silver/transactions.parquet')
GROUP BY 1
ORDER BY 1;
