-- The GnuCash account hierarchy and the payee→account mapping were used
-- to write a predicted destination account into the OFX MEMO field.
-- GnuCash discards MEMO on import, so the mapping never reached the book
-- and classifying transactions during capture saved no downstream work.
DROP TABLE IF EXISTS gnucash_account;
ALTER TABLE payee DROP COLUMN default_account;
