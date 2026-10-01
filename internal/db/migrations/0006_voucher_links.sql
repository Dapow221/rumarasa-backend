-- Vouchers for people who aren't members: the owner shares a secret link and
-- the customer redeems it themselves on the website, leaving their details.
-- A voucher is either locked to a member or shared by link, never both.
ALTER TABLE vouchers
    ADD COLUMN link_token      varchar(64) UNIQUE, -- random, unguessable; NULL = no link
    ADD COLUMN recipient_name  varchar(200),
    ADD COLUMN recipient_phone varchar(20),
    ADD COLUMN recipient_email varchar(254),
    ADD COLUMN redeemed_via    varchar(10) CHECK (redeemed_via IN ('cashier', 'link')),
    ADD CONSTRAINT vouchers_member_or_link CHECK (link_token IS NULL OR member_id IS NULL);

UPDATE vouchers SET redeemed_via = 'cashier' WHERE status = 'redeemed';
