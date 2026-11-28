// The Team page (team admins only): the team's day with a member drill-down,
// WFH requests to decide, and the week / month report with the CSV export.
// The bot checks admin rights on every call.

import {FormError, decideWFH, exportCSV, fetchAdminWFH, fetchMember, fetchTeamDay, fetchTeamReport} from 'api';
import type {AdminWFH, AdminWFHItem, MemberDetail, TeamDay, TeamMemberDay, TeamReport} from 'api';
import {hm, timeOfDay} from 'format';
import React, {useCallback, useEffect, useState} from 'react';

import Markdown from './Markdown';
import {useRefresh} from './refresh';
import {LoadState} from './Status';

type Tab = 'today' | 'wfh' | 'reports';

const POLL_MS = 60 * 1000; // members' changes don't send admins events

const STATE: Record<string, {label: string; dot: string}> = {
    working: {label: 'Working', dot: 'on'},
    break: {label: 'On break', dot: 'break'},
    out: {label: 'Checked out', dot: 'off'},
    absent: {label: 'Not checked in', dot: 'off'},
    elsewhere: {label: 'Other org', dot: 'off'},
};

const EOD: Record<string, {label: string; key: string}> = {
    sent: {label: 'EOD ✓', key: 'progress'},
    missing: {label: 'No EOD', key: 'overdue'},
    waiting: {label: 'EOD later', key: 'todo'},
};

// usePoll loads now, every POLL_MS while shown, and on `events`.
function usePoll<T>(fetcher: () => Promise<T>, events: string[]) {
    const [data, setData] = useState<T | null>(null);
    const [failed, setFailed] = useState('');
    const load = useCallback(async () => {
        try {
            setData(await fetcher());
            setFailed('');
        } catch (e) {
            setFailed((e as Error).message);
        }
    }, [fetcher]);
    useEffect(() => {
        load();
        const id = window.setInterval(load, POLL_MS);
        return () => window.clearInterval(id);
    }, [load]);
    useRefresh(events, load);
    return {data, failed, load, setData};
}

function MemberRow({m, onOpen}: {m: TeamMemberDay; onOpen: (m: TeamMemberDay) => void}) {
    const st = STATE[m.state] ?? STATE.absent;
    const eod = EOD[m.eod];
    let detail = st.label;
    if (m.state === 'working' || m.state === 'break') {
        detail += m.since ? ` · since ${timeOfDay(m.since)}` : '';
        detail += m.mode === 'wfh' ? ' · 🏠' : '';
    } else if (m.state === 'elsewhere' && m.elsewhere) {
        detail = `Working in ${m.elsewhere}`;
    }
    return (
        <li>
            <button
                className='att-member'
                onClick={() => onOpen(m)}
                aria-label={`@${m.username}: ${detail}`}
            >
                <span className={`att-dot att-dot--${st.dot}`}/>
                <span className='att-member-main'>
                    <span className='att-member-name'>{`@${m.username}`}</span>
                    <span className='att-muted att-small'>{detail}</span>
                </span>
                <span className='att-member-side'>
                    <span className='att-member-hours'>{m.seconds ? hm(m.seconds) : '–'}</span>
                    <span className='att-member-badges'>
                        {eod && <span className={`att-pill att-pill--${eod.key}`}>{eod.label}</span>}
                        {m.blocked > 0 && <span className='att-pill att-pill--blocked'>{`🚧 ${m.blocked}`}</span>}
                    </span>
                </span>
            </button>
        </li>
    );
}

function MemberView({teamId, member, onBack}: {teamId: string; member: TeamMemberDay; onBack: () => void}) {
    const [d, setD] = useState<MemberDetail | null>(null);
    const [failed, setFailed] = useState('');
    const load = useCallback(() => {
        setFailed('');
        fetchMember(teamId, member.user_id).then(setD).catch((e: Error) => setFailed(e.message));
    }, [teamId, member.user_id]);
    useEffect(load, [load]);

    let report: React.ReactNode = (
        <LoadState
            failed={failed}
            onRetry={load}
        />
    );
    if (d) {
        if (d.report) {
            report = (
                <>
                    <Markdown text={d.report}/>
                    <div className='att-muted att-small'>{`Sent ${d.submitted_at ? timeOfDay(d.submitted_at) : ''} · ${d.sync_line ?? ''}`}</div>
                </>
            );
        } else {
            let why = 'Didn’t work in this team today.';
            if (member.eod === 'waiting') {
                why = 'Still checked in: the report opens when they check out.';
            } else if (d.worked) {
                why = '❌ Worked today but hasn’t sent an end-of-day report.';
            }
            report = <div className='att-empty'>{why}</div>;
        }
    }
    let tasks: React.ReactNode = null;
    if (d?.tasks) {
        let body: React.ReactNode;
        if (!d.tasks.linked) {
            body = <div className='att-empty'>{'GitHub not linked.'}</div>;
        } else if (d.tasks.error) {
            body = <div className='att-empty'>{d.tasks.error}</div>;
        } else if (d.tasks.tasks.length === 0) {
            body = <div className='att-empty'>{'No open tasks.'}</div>;
        } else {
            body = (
                <ul className='att-plain-list'>
                    {d.tasks.tasks.map((t) => (
                        <li key={t.url}>
                            {t.picked ? '▶ ' : '○ '}
                            {/* Mattermost's ExternalLink component isn't available to plugins; rel=noopener noreferrer keeps the new tab safe. */}
                            {/* eslint-disable-next-line @mattermost/use-external-link */}
                            <a
                                href={t.url}
                                target='_blank'
                                rel='noopener noreferrer'
                            >{t.title}</a>
                            <span className='att-muted'>{` · ${t.status || 'No status'}${t.overdue ? ' · overdue' : ''}`}</span>
                        </li>
                    ))}
                </ul>
            );
        }
        tasks = (
            <section className='att-card att-card--plain'>
                <h3 className='att-h'>{'Open tasks'}</h3>
                {body}
            </section>
        );
    }

    return (
        <div className='att-page-body'>
            <button
                className='att-back'
                onClick={onBack}
            >{'← Team'}</button>
            <h3 className='att-h'>{`@${member.username}`}</h3>
            <div className='att-muted att-small'>{`${(STATE[member.state] ?? STATE.absent).label} · ${hm(member.seconds)} today`}</div>
            <section
                className='att-card att-card--plain'
                aria-label='End-of-day report'
            >
                <h3 className='att-h'>{`End-of-day report${d ? ` · ${d.day_label}` : ''}`}</h3>
                {report}
            </section>
            {tasks}
        </div>
    );
}

function TodayTab({teamId, onCount}: {teamId: string; onCount: (n: number) => void}) {
    const fetcher = useCallback(() => fetchTeamDay(teamId), [teamId]);
    const {data, failed, load} = usePoll<TeamDay>(fetcher, []);
    const [open, setOpen] = useState<TeamMemberDay | null>(null);
    useEffect(() => {
        if (data) {
            onCount(data.wfh_pending);
        }
    }, [data, onCount]);

    if (open) {
        return (
            <MemberView
                teamId={teamId}
                member={open}
                onBack={() => setOpen(null)}
            />
        );
    }
    if (!data) {
        return (
            <LoadState
                failed={failed}
                onRetry={load}
            />
        );
    }
    return (
        <>
            <div
                className='att-stats'
                aria-label='Team summary'
            >
                <span><strong>{data.working}</strong>{' working'}</span>
                <span><strong>{data.on_break}</strong>{' on break'}</span>
                <span><strong>{data.out}</strong>{' out'}</span>
                <span><strong>{data.absent}</strong>{' not in'}</span>
                <span className={data.eod_sent < data.eod_due ? 'att-warn-text' : ''}><strong>{`${data.eod_sent}/${data.eod_due}`}</strong>{' EOD'}</span>
            </div>
            {failed && <div className='att-error'>{failed}</div>}
            <ul
                className='att-members'
                aria-label={`Team · ${data.day_label}`}
            >
                {data.members.map((m) => (
                    <MemberRow
                        key={m.user_id}
                        m={m}
                        onOpen={setOpen}
                    />
                ))}
            </ul>
            <div className='att-muted att-small'>{'Updates every minute. Tap someone for their report and tasks.'}</div>
        </>
    );
}

function WFHRow({item, teamId, onDone}: {item: AdminWFHItem; teamId: string; onDone: (w: AdminWFH) => void}) {
    const [rejecting, setRejecting] = useState(false);
    const [note, setNote] = useState('');
    const [error, setError] = useState('');
    const [busy, setBusy] = useState(false);
    const decide = async (approve: boolean) => {
        setBusy(true);
        setError('');
        try {
            onDone(await decideWFH(teamId, item.id, approve, note.trim()));
        } catch (e) {
            setError(e instanceof FormError ? Object.values(e.fields)[0] ?? e.message : (e as Error).message);
        } finally {
            setBusy(false);
        }
    };
    return (
        <li className='att-wfh-item'>
            <div className='att-row'>
                <strong>{`@${item.username}`}</strong>
                {item.urgent && <span className='att-pill att-pill--overdue'>{'Urgent'}</span>}
            </div>
            <div>{`${item.dates}${item.half_day ? ' · ½ day' : ''}`}</div>
            <div className='att-muted att-small'>{item.reason}</div>
            {item.used_month && <div className='att-muted att-small'>{`WFH days approved this month: ${item.used_month}`}</div>}
            {rejecting ? (
                <div className='att-field'>
                    <textarea
                        className='att-input'
                        rows={2}
                        maxLength={500}
                        value={note}
                        onChange={(e) => setNote(e.target.value)}
                        placeholder='Reason (sent to the member)'
                        aria-label={`Reason for rejecting @${item.username}`}
                    />
                    <div className='att-actions'>
                        <button
                            className='att-btn att-btn--danger att-btn--small'
                            disabled={busy}
                            onClick={() => decide(false)}
                        >{'Reject'}</button>
                        <button
                            className='att-link'
                            onClick={() => setRejecting(false)}
                        >{'Cancel'}</button>
                    </div>
                </div>
            ) : (
                <div className='att-actions'>
                    <button
                        className='att-btn att-btn--primary att-btn--small'
                        disabled={busy}
                        onClick={() => decide(true)}
                    >{'✅ Approve'}</button>
                    <button
                        className='att-btn att-btn--small'
                        disabled={busy}
                        onClick={() => setRejecting(true)}
                    >{'❌ Reject…'}</button>
                </div>
            )}
            {error && <div className='att-field-error'>{error}</div>}
        </li>
    );
}

function WFHTab({teamId, onCount}: {teamId: string; onCount: (n: number) => void}) {
    const fetcher = useCallback(() => fetchAdminWFH(teamId), [teamId]);
    const {data, failed, load, setData} = usePoll<AdminWFH>(fetcher, ['wfh_changed']);
    useEffect(() => {
        if (data) {
            onCount(data.pending.length);
        }
    }, [data, onCount]);
    if (!data) {
        return (
            <LoadState
                failed={failed}
                onRetry={load}
            />
        );
    }
    return (
        <>
            {data.today.length > 0 && <div className='att-small'>{`🏠 WFH today: ${data.today.join(', ')}`}</div>}
            <section
                className='att-card att-card--plain'
                aria-label='Pending requests'
            >
                <h3 className='att-h'>{`Pending · ${data.pending.length}`}</h3>
                {data.pending.length === 0 ? (
                    <div className='att-empty'>{'Nothing to decide. 🎉'}</div>
                ) : (
                    <ul className='att-tasks'>
                        {data.pending.map((r) => (
                            <WFHRow
                                key={r.id}
                                item={r}
                                teamId={teamId}
                                onDone={setData}
                            />
                        ))}
                    </ul>
                )}
            </section>
            {data.recent.length > 0 && (
                <section
                    className='att-card att-card--plain'
                    aria-label='Last 30 days'
                >
                    <h3 className='att-h'>{'Last 30 days'}</h3>
                    <ul className='att-plain-list'>
                        {data.recent.map((r) => (
                            <li key={r.id}>
                                {`@${r.username} · ${r.dates} · `}
                                <span className='att-muted'>
                                    {r.status}
                                    {r.decided_by ? ` by @${r.decided_by}` : ''}
                                    {r.note ? `: ${r.note}` : ''}
                                </span>
                            </li>
                        ))}
                    </ul>
                </section>
            )}
        </>
    );
}

function ReportsTab({teamId}: {teamId: string}) {
    const [range, setRange] = useState<'week' | 'month'>('week');
    const [data, setData] = useState<TeamReport | null>(null);
    const [failed, setFailed] = useState('');
    const [exportMsg, setExportMsg] = useState('');
    const [exporting, setExporting] = useState(false);
    const load = useCallback(() => {
        setFailed('');
        fetchTeamReport(teamId, range).then(setData).catch((e: Error) => setFailed(e.message));
    }, [teamId, range]);
    useEffect(() => {
        setData(null);
        load();
    }, [load]);

    const doExport = async () => {
        setExporting(true);
        try {
            setExportMsg((await exportCSV(teamId)).message);
        } catch (e) {
            setExportMsg((e as Error).message);
        } finally {
            setExporting(false);
        }
    };

    return (
        <>
            <div className='att-row'>
                <div
                    className='att-seg att-seg--tabs'
                    role='tablist'
                    aria-label='Report range'
                >
                    {(['week', 'month'] as const).map((r) => (
                        <button
                            key={r}
                            role='tab'
                            aria-selected={range === r}
                            className={`att-seg-btn${range === r ? ' is-on' : ''}`}
                            onClick={() => setRange(r)}
                        >{r === 'week' ? 'This week' : 'This month'}</button>
                    ))}
                </div>
                <button
                    className='att-btn att-btn--small'
                    disabled={exporting}
                    onClick={doExport}
                    title='This month’s sessions as a CSV file, sent to you by DM'
                >{exporting ? 'Sending…' : '⬇ CSV to my DMs'}</button>
            </div>
            {exportMsg && (
                <div
                    className='att-notice'
                    role='status'
                ><Markdown text={exportMsg}/></div>
            )}
            {!data && (
                <LoadState
                    failed={failed}
                    onRetry={load}
                />
            )}
            {data && (
                <>
                    <div className='att-muted att-small'>{data.label}</div>
                    {data.eod_due > 0 && (
                        <div className='att-stat'>
                            <span className='att-big'>{`${Math.round((data.eod_sent / data.eod_due) * 100)}%`}</span>
                            <span className='att-muted'>{`end-of-day reports sent · ${data.eod_sent} of ${data.eod_due} days worked`}</span>
                        </div>
                    )}
                    {data.rows.length === 0 ? (
                        <div className='att-empty'>{'No tracked members, and no one logged time here in this period.'}</div>
                    ) : (
                        <div className='att-table-wrap'>
                            <table className='att-table'>
                                <thead>
                                    <tr>
                                        <th>{'Member'}</th>
                                        <th>{'Days'}</th>
                                        <th>{'Here'}</th>
                                        <th>{'All orgs'}</th>
                                        <th>{'Expected'}</th>
                                        <th>{'WFH'}</th>
                                        <th title='End-of-day reports sent, of the days worked here'>{'EOD'}</th>
                                    </tr>
                                </thead>
                                <tbody>
                                    {data.rows.map((r) => (
                                        <tr key={r.username}>
                                            <td>{`@${r.username}${r.short ? ' ⚠️' : ''}`}</td>
                                            <td>{r.days}</td>
                                            <td>{hm(r.team_seconds)}</td>
                                            <td>{hm(r.all_seconds)}</td>
                                            <td>{r.has_expected ? hm(r.expected_seconds) : '—'}</td>
                                            <td>{r.wfh_days}</td>
                                            <td className={r.eod_sent < r.eod_due ? 'att-warn-text' : ''}>{r.eod_due ? `${r.eod_sent}/${r.eod_due}` : '—'}</td>
                                        </tr>
                                    ))}
                                </tbody>
                            </table>
                        </div>
                    )}
                    <div className='att-muted att-small'>{'⚠️ = all-orgs hours under 90% of expected (up to yesterday). “—” = not tracked. EOD = reports sent of the days worked here (today counts after check-out).'}</div>
                </>
            )}
        </>
    );
}

export default function TeamPage({teamId}: {teamId: string}) {
    const [tab, setTab] = useState<Tab>('today');
    const [pending, setPending] = useState(0);
    const tabs: Array<{key: Tab; label: string}> = [
        {key: 'today', label: 'Today'},
        {key: 'wfh', label: pending ? `WFH · ${pending}` : 'WFH'},
        {key: 'reports', label: 'Reports'},
    ];
    return (
        <div className='att-page-body'>
            <div
                className='att-seg att-seg--tabs'
                role='tablist'
                aria-label='Team views'
            >
                {tabs.map((t) => (
                    <button
                        key={t.key}
                        role='tab'
                        aria-selected={tab === t.key}
                        className={`att-seg-btn${tab === t.key ? ' is-on' : ''}`}
                        onClick={() => setTab(t.key)}
                    >{t.label}</button>
                ))}
            </div>
            {tab === 'today' && (
                <TodayTab
                    teamId={teamId}
                    onCount={setPending}
                />
            )}
            {tab === 'wfh' && (
                <WFHTab
                    teamId={teamId}
                    onCount={setPending}
                />
            )}
            {tab === 'reports' && <ReportsTab teamId={teamId}/>}
        </div>
    );
}
