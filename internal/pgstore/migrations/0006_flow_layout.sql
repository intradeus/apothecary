-- Flow builder layout (DESIGN3 §2.2): node positions are display metadata,
-- stored beside the document and EXCLUDED from the content hash — moving a
-- node never creates a flow version.
ALTER TABLE flows ADD COLUMN layout jsonb;
