package bot

// All customer-facing strings live here, which makes translating the bot
// to another language a single-file change.
const (
	btnBook     = "📅 Book a visit"
	btnMy       = "📋 My bookings"
	btnInfo     = "ℹ️ Info"
	btnSchedule = "🛠 Schedule"

	txtWelcome = "Hi, %s! 👋\nI'm the booking assistant of <b>%s</b>.\n\n" +
		"Book a visit in a few taps — I'll remind you before it starts."
	txtHelp          = "Use the buttons below to book a visit or manage your bookings."
	txtChooseService = "<b>Step 1/3.</b> Choose a service:"
	txtChooseDay     = "<b>Step 2/3.</b> %s — pick a day:"
	txtNoDays        = "😔 Sorry, there are no free slots for <b>%s</b> in the next %d days."
	txtChooseTime    = "<b>Step 3/3.</b> %s on <b>%s</b> — pick a time:"
	txtNoTimes       = "😔 This day is fully booked. Please pick another day."
	txtConfirm       = "Please check your booking:\n\n💈 <b>%s</b> (%d min, %s)\n🗓 <b>%s</b>\n\nConfirm?"
	txtBooked        = "✅ You're booked!\n\n💈 <b>%s</b>\n🗓 <b>%s</b>\n\nI'll send you a reminder before the visit."
	txtSlotTaken     = "⚡ Someone has just taken this slot. Please choose another time."
	txtExpired       = "⌛ This booking session has expired. Let's start again."
	txtAborted       = "Booking cancelled. Tap «" + btnBook + "» whenever you're ready."
	txtNoBookings    = "You have no upcoming bookings."
	txtYourBookings  = "<b>Your upcoming bookings:</b>"
	txtCancelled     = "🗑 Booking cancelled: %s, %s."
	txtNotFound      = "This booking no longer exists or was already cancelled."
	txtInfo          = "<b>%s</b>\n🕙 Open daily %02d:00–%02d:00\n\n<b>Services:</b>\n%s"
	txtSlowDown      = "Too many requests, slow down a little 🙂"
	txtError         = "Something went wrong on our side. Please try again in a minute."
	txtReminder      = "⏰ Reminder: your visit to <b>%s</b> is coming up!\n\n💈 %s\n🗓 <b>%s</b>\n\nCan't make it? Cancel in «" + btnMy + "»."

	txtAdminNew       = "🆕 New booking #%d\n👤 %s\n💈 %s\n🗓 %s"
	txtAdminCancelled = "❌ Customer cancelled booking #%d\n👤 %s\n💈 %s\n🗓 %s"
	txtAdminPick      = "<b>Schedule.</b> Which period?"
	txtAdminEmpty     = "No bookings for %s."
	txtAdminList      = "<b>Bookings for %s:</b>\n\n%s"
	txtCancelledByBiz = "😔 Unfortunately, your booking was cancelled by %s:\n💈 %s, %s\nPlease book another time."

	btnConfirm = "✅ Confirm"
	btnAbort   = "✖️ Cancel"
	btnBack    = "« Back"
)
