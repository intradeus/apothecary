-- A personal token's grant. actions NULL is a full token (the account
-- itself): every token minted before this reads back as one. Set, the
-- token may do only those actions; project_ids, set, only in those
-- workshops.

ALTER TABLE api_tokens ADD COLUMN actions text[];
ALTER TABLE api_tokens ADD COLUMN project_ids text[];
