-- The shape of the answer a ping takes (decision|choice|multi|text), declared
-- on the ping instead of inferred from its type and options. Rows from
-- before are read back with the inference (run.PingView.AnswerKind).
ALTER TABLE pings ADD COLUMN answer_kind text NOT NULL DEFAULT '';
