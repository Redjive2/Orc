# §1 Mailman

Mailman is a minimal inter-process mail service. All it does is:

- let you send mail (as markdown)
- store said mail, with timestamps, and (non-blind) CC functionality
- let you archive mail

Mail is sent between users. Users can be created and destroyed easily;
authentication happens on every request via a privately stored (by the process)
key.

There is functionality for checking unread messages.

User accounts are controlled via Orc.
