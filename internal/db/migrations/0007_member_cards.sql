-- Digital membership cards: each member can be given a secret link that shows
-- their card. Re-creating the link rotates the token so a leaked one dies.
ALTER TABLE members
    ADD COLUMN card_token   varchar(64) UNIQUE, -- random, unguessable; NULL = no card link
    ADD COLUMN card_sent_at timestamptz;
