# Manual test script

A step-by-step check in Mattermost after a deploy. It needs one **team admin** (A) and one **member** (M) of the same team, both with a GitHub login linked and in the team's GitHub org, and one test issue assigned to each. Run it on a workday. Everything a step shows is private (only visible to you) unless it says otherwise.

Before you start, on the server:

```bash
docker compose ps attendance            # Up … (healthy)
docker compose logs attendance | grep -E "migration applied|github project"
```

✅ `github project ok` for every team with GitHub, no `ERROR` lines.

## 1. Attendance basics

| # | Who | Do | Expect |
|---|---|---|---|
| 1.1 | M | `/att in` | ✅ Checked in … · 🏢 Office, with ☕ Break / 🔴 Check out buttons. Custom status 🟢 *Working*. |
| 1.2 | M | `/att in` again | "You're already checked in …" |
| 1.3 | M | ☕ Break, then ▶️ Back | Status ☕ *On break*, then 🟢 again. |
| 1.4 | M | `/att status` | M listed with check-in time. |
| 1.5 | M | `/att hours` | Today's hours, per organization. |
| 1.6 | M | `/att help` | Member commands only, every row readable. |
| 1.7 | A | `/att help` | Also the admin table, every row readable. |

## 2. GitHub link and tasks

| # | Who | Do | Expect |
|---|---|---|---|
| 2.1 | M | `/att github` | Shows M's linked login. |
| 2.2 | M | `/att github someone-else` | 🔒 Only admins can link GitHub accounts … |
| 2.3 | M | `/att tasks` | M's open issues on the team's Project (⚠️ overdue, 🚧 blocked) and a **Pick today's task** menu. |
| 2.4 | A | `/att member @M github nobody-xyz-123` | "There's no GitHub user …" — nothing changes. |

## 3. Start of day (picks)

Create a new issue in the team's `operations` repo assigned to M. ✅ Within seconds it's on the Project as **Todo** (Auto-add).

| # | Who | Do | Expect |
|---|---|---|---|
| 3.1 | M | `/att out`, then `/att in` | The check-in reply lists M's tasks with the pick menu. |
| 3.2 | M | Pick the new issue | "▶️ #N … is now **In progress**". On the Project: In progress. |
| 3.3 | A | On the Project, move another of M's issues to **In review**. M picks it from `/att tasks`. | "📌 … is already **In review**, so it's left as is." Project unchanged. |

## 4. End-of-day report and GitHub sync

| # | Who | Do | Expect |
|---|---|---|---|
| 4.1 | M | `/att out` | The **End of day report** form opens. Picked issues have their own status row; other Todo issues are under **Other tasks I finished**. |
| 4.2 | M | Close the form | Nothing saved. The check-out reply's **📝 End-of-day report** button reopens it. |
| 4.3 | M | Set the picked issue to **In review**, Blockers `Waiting for review`, fill *What I did* (10+ chars), submit | In `#attendance`: a thread *📝 EOD reports — <day>* with M's report as a reply (open the thread). Within ~10 s the reply gets a *GitHub:* line. |
| 4.4 | — | Check GitHub | Project: In review. Issue comment *📝 EOD … **In review*** by the bot. |
| 4.5 | A | Check `#attendance-admin` | 🚧 blocker alert for M with a link to the report. |
| 4.6 | M | `/att eod submit`, set it to **Blocked**, submit | Issue gets the **blocked** label and a 2nd comment. The admin alert is **edited** (not a new post) and lists it as Blocked. The thread reply is edited too. |
| 4.7 | M | `/att eod submit`, set it to **Done**, clear Blockers, submit | Issue **closed**, label removed, 3rd comment, Project Done. Admin alert: "✅ … no longer lists blockers". |
| 4.8 | M | `/att eod` | The report, with **GitHub: ✅ synced**. |
| 4.9 | M | `/att eod submit` and pick an issue under **Other tasks I finished** | That issue closes with a comment. |

## 5. Admin views

| # | Who | Do | Expect |
|---|---|---|---|
| 5.1 | M | `/att eod team` | 🔒 Only admins … (same for `/att eod @A` and `/att tasks @A`) |
| 5.2 | A | `/att eod team` | Table: M with hours, ✅, Done/Blocked counts, GitHub ✅; "Sent: x of y"; link to the thread. |
| 5.3 | A | `/att eod @M` | M's report with an *open in thread* link. |
| 5.4 | A | `/att eod @M 2020-01-01` | "… didn't work in … on …" |
| 5.5 | A | `/att tasks @M` | M's open tasks (no pick menu). |

## 6. Scheduled jobs (same day)

Set a **Due date** in the past on one open issue of M's before 10:00. Have a second member check in and out **without** submitting a report.

| Time | Where | Expect |
|---|---|---|
| 10:00 | `#attendance-admin` | *🗂️ Task check*: ⚠️ Overdue lists M's issue; 😴 lists linked members with no open task. Nothing is posted if there's nothing to report. |
| 19:30 | DM to the second member | "📝 You haven't sent today's end-of-day report for …" — and no DM to anyone who sent one or is still checked in. |
| 20:00 | `#attendance` | Daily summary with **EOD / Done / Blocked** columns (✅ / ❌ / ⏳) and a *📝 Today's end-of-day reports* link. |
| Mon 09:00 | `#attendance-admin` | Weekly report followed by **Tasks and end-of-day reports** (EOD sent ÷ days worked, issues completed, overdue now). |

Check the jobs ran:

```bash
docker compose logs --since 24h attendance | grep -E '"job":"(task_alerts|eod_reminder|daily_summary|weekly_report)"'
```

✅ `job done` for each, no `job failed`.

## 7. Failure cases

| # | Do | Expect |
|---|---|---|
| 7.1 | Temporarily set `GITHUB_APP_ID` to a wrong value, restart, M runs `/att in`, `/att out`, submits a report | Check-in and check-out work normally; check-in says *Couldn't load your tasks right now*; the form opens without tasks; the report is saved and posted. **Put the right value back and restart.** |
| 7.2 | Ask an admin to unassign M from an issue after M opened the form, then submit a status for it | Report saved; `/att eod` shows *GitHub: ✅ synced (#N skipped: not assigned to you)*; nothing written to that issue on GitHub. |
| 7.3 | `docker compose restart attendance` in the middle of the day | Nothing is sent twice (job log says `job already ran`); pending EOD syncs finish within 15 minutes. |

## Clean up

Close or delete the test issues, and unlink test accounts with `/att member @user github none` if needed.
