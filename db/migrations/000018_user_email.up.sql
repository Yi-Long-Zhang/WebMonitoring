-- The person an incident is assigned to is not reachable today: users carry no
-- contact address at all, so "assign this to Zhang" notifies nobody, and a
-- high-risk incident nobody claims stays silent until someone happens to look at
-- the list.
--
-- Empty is the default rather than NOT NULL-without-default because every
-- existing row has no address to backfill, and an address is optional: a
-- deployment that delivers only through its alert channels keeps working, it
-- just does not get the direct notices.
ALTER TABLE users ADD COLUMN email text NOT NULL DEFAULT '';
