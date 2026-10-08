package mail

import (
	"bytes"
	"fmt"
	"html/template"
	"strings"
)

var tierNames = map[string]string{"silver": "Silver", "gold": "Gold", "platinum": "Platinum"}

// TierName turns "gold" into "Gold".
func TierName(tier string) string {
	if n, ok := tierNames[tier]; ok {
		return n
	}
	return tier
}

const layout = `<!doctype html>
<html lang="id">
<head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>{{.Subject}}</title></head>
<body style="margin:0;padding:0;background:#faf6ef;">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="background:#faf6ef;">
<tr><td align="center" style="padding:32px 16px;">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="max-width:560px;background:#fffdf8;border:1px solid #e7dccb;">
<tr><td style="padding:32px 32px 8px;text-align:center;font-family:Georgia,'Times New Roman',serif;">
<div style="font-size:13px;letter-spacing:4px;color:#2a1b10;">RUMARASA <span style="color:#b06a2e;">NUSANTARA</span></div>
<div style="margin-top:6px;font-size:13px;font-style:italic;color:#b06a2e;">Keluarga Rumarasa</div>
</td></tr>
<tr><td style="padding:16px 32px 32px;font-family:Georgia,'Times New Roman',serif;font-size:16px;line-height:1.65;color:#2a1b10;">
<p style="margin:0 0 16px;">Yth. Bapak/Ibu {{.Name}},</p>
{{.Body}}
<p style="margin:24px 0 0;">Hormat kami,<br><strong>Rumarasa Nusantara</strong></p>
</td></tr>
<tr><td style="padding:16px 32px;border-top:1px solid #e7dccb;text-align:center;font-family:Arial,Helvetica,sans-serif;font-size:12px;line-height:1.6;color:#8a7a68;">
Email ini dikirim secara otomatis, mohon tidak membalas email ini.<br>
Untuk pertanyaan, hubungi kami melalui <a href="{{.SiteURL}}" style="color:#b06a2e;">rumarasanusantara.com</a>.
</td></tr>
</table>
</td></tr>
</table>
</body>
</html>`

var layoutTmpl = template.Must(template.New("layout").Parse(layout))

func render(subject, name, siteURL string, body template.HTML) (string, error) {
	var buf bytes.Buffer
	err := layoutTmpl.Execute(&buf, map[string]any{
		"Subject": subject, "Name": name, "SiteURL": siteURL, "Body": body,
	})
	return buf.String(), err
}

func paragraphs(lines ...string) template.HTML {
	var b strings.Builder
	for _, l := range lines {
		fmt.Fprintf(&b, `<p style="margin:0 0 16px;">%s</p>`, l)
	}
	return template.HTML(b.String())
}

// SignupReceived confirms a membership application while it waits for approval.
func SignupReceived(to, name, tier, siteURL string) (Message, error) {
	const subject = "Pendaftaran Member Rumarasa Nusantara Telah Diterima"
	esc := template.HTMLEscapeString
	html, err := render(subject, name, siteURL, paragraphs(
		"Terima kasih telah mendaftar sebagai member <strong>Keluarga Rumarasa</strong>.",
		fmt.Sprintf("Pendaftaran Anda untuk tier <strong>%s</strong> telah kami terima dan saat ini sedang diverifikasi oleh tim kami.", esc(TierName(tier))),
		"Setelah pendaftaran disetujui, kami akan mengirimkan nomor member beserta kartu member digital Anda melalui email ini.",
	))
	if err != nil {
		return Message{}, err
	}
	text := fmt.Sprintf(`Yth. Bapak/Ibu %s,

Terima kasih telah mendaftar sebagai member Keluarga Rumarasa.

Pendaftaran Anda untuk tier %s telah kami terima dan saat ini sedang diverifikasi oleh tim kami.

Setelah pendaftaran disetujui, kami akan mengirimkan nomor member beserta kartu member digital Anda melalui email ini.

Hormat kami,
Rumarasa Nusantara

Email ini dikirim secara otomatis, mohon tidak membalas email ini.`, name, TierName(tier))
	return Message{To: to, Subject: subject, HTML: html, Text: text}, nil
}

// MemberCard welcomes an approved member and carries their card: inline as
// an image when one is given, and always as a link.
func MemberCard(to, name, memberNo, tier, cardURL, siteURL string, cardPNG []byte) (Message, error) {
	const subject = "Selamat Datang di Keluarga Rumarasa — Kartu Member Anda"
	esc := template.HTMLEscapeString
	var card string
	var attachments []Attachment
	if len(cardPNG) > 0 {
		card = fmt.Sprintf(`<p style="margin:8px 0 20px;text-align:center;"><img src="cid:kartu-member" width="496" alt="Kartu member %s" style="display:block;width:100%%;max-width:496px;height:auto;border-radius:16px;"></p>`, esc(memberNo))
		attachments = []Attachment{{Filename: "kartu-member-" + memberNo + ".png", Content: cardPNG, ContentID: "kartu-member"}}
	}
	body := paragraphs(
		"Selamat! Keanggotaan Anda di <strong>Keluarga Rumarasa</strong> telah aktif. Berikut kartu member digital Anda:",
	) + template.HTML(card) + template.HTML(fmt.Sprintf(`
<table role="presentation" cellpadding="0" cellspacing="0" style="margin:0 0 20px;font-size:15px;">
<tr><td style="padding:2px 16px 2px 0;color:#8a7a68;">No. Member</td><td style="padding:2px 0;letter-spacing:2px;"><strong>%s</strong></td></tr>
<tr><td style="padding:2px 16px 2px 0;color:#8a7a68;">Tier</td><td style="padding:2px 0;"><strong>%s</strong></td></tr>
</table>
<p style="margin:0 0 20px;text-align:center;"><a href="%s" style="display:inline-block;padding:13px 28px;background:#2a1b10;color:#f5e9d7;text-decoration:none;border-radius:999px;font-family:Arial,Helvetica,sans-serif;font-size:13px;letter-spacing:2px;">LIHAT KARTU MEMBER</a></p>`,
		esc(memberNo), esc(TierName(tier)), esc(cardURL))) + paragraphs(
		"Silakan tunjukkan kartu ini kepada kasir setiap kali Anda berkunjung untuk menikmati benefit member. Kartu juga dapat disimpan ke galeri ponsel Anda melalui tautan di atas.",
		"Kami menantikan kunjungan Anda berikutnya.",
	)
	html, err := render(subject, name, siteURL, body)
	if err != nil {
		return Message{}, err
	}
	text := fmt.Sprintf(`Yth. Bapak/Ibu %s,

Selamat! Keanggotaan Anda di Keluarga Rumarasa telah aktif.

No. Member : %s
Tier       : %s
Kartu      : %s

Silakan tunjukkan kartu ini kepada kasir setiap kali Anda berkunjung untuk menikmati benefit member.

Kami menantikan kunjungan Anda berikutnya.

Hormat kami,
Rumarasa Nusantara

Email ini dikirim secara otomatis, mohon tidak membalas email ini.`, name, memberNo, TierName(tier), cardURL)
	return Message{To: to, Subject: subject, HTML: html, Text: text, Attachments: attachments}, nil
}
