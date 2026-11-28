// The panel's home screen: a greeting, the status bar with the main action,
// and a tile per page with a live one-line summary.

import {fetchHours, fetchTasks, fetchTeamDay, fetchWFH} from 'api';
import type {Hours, TasksState, TeamDay, WFHState} from 'api';
import {hm} from 'format';
import React, {useCallback, useEffect, useRef, useState} from 'react';

import {useNow} from './hooks';
import {useRefresh} from './refresh';
import {liveSeconds} from './session';
import type {SessionProps} from './session';
import StatusBar from './StatusBar';

export type Page = 'home' | 'attendance' | 'tasks' | 'eod' | 'wfh' | 'hours' | 'settings' | 'team';

// greeting: "Good morning" / "afternoon" / "evening" by the local hour.
export function greeting(hour: number): string {
    if (hour < 12) {
        return 'Good morning';
    }
    return hour < 17 ? 'Good afternoon' : 'Good evening';
}

// useLoad fetches on mount / team change and again on `events`.
function useLoad<T>(fetcher: (() => Promise<T>) | null, events: string[]) {
    const [data, setData] = useState<T | null>(null);
    const load = useCallback(async () => {
        if (!fetcher) {
            return;
        }
        try {
            setData(await fetcher());
        } catch {
            setData(null); // the tile just shows less
        }
    }, [fetcher]);
    useEffect(() => {
        setData(null);
        load();
    }, [load]);
    useRefresh(events, load, Boolean(fetcher));
    return data;
}

type TileProps = {icon: string; title: string; summary: React.ReactNode; tone?: 'warn' | 'ok' | ''; focus?: boolean; onOpen: () => void};

function Tile({icon, title, summary, tone = '', focus = false, onOpen}: TileProps) {
    const ref = useRef<HTMLButtonElement>(null);
    useEffect(() => {
        if (focus) {
            ref.current?.focus();
        }
    }, [focus]);
    return (
        <button
            ref={ref}
            className={`att-tile${tone ? ` att-tile--${tone}` : ''}`}
            onClick={onOpen}
        >
            <span
                className='att-tile-icon'
                aria-hidden='true'
            >{icon}</span>
            <span className='att-tile-title'>{title}</span>
            <span className='att-tile-sub'>{summary}</span>
        </button>
    );
}

type Props = SessionProps & {message: React.ReactNode; onOpen: (page: Page) => void; focusTile?: Page | null};

export default function Home(props: Props) {
    const {me, onOpen, message, focusTile} = props;
    const tile = (p: Page) => ({focus: focusTile === p, onOpen: () => onOpen(p)});
    const teamId = me.team.id;
    const now = useNow(Boolean(me.session));
    const t = liveSeconds(props, now);

    const tasksFetcher = useCallback(() => fetchTasks(teamId), [teamId]);
    const wfhFetcher = useCallback(() => fetchWFH(teamId), [teamId]);
    const hoursFetcher = useCallback(() => fetchHours(teamId, 'week'), [teamId]);
    const tasks = useLoad<TasksState>(me.github_set_up ? tasksFetcher : null, ['tasks_changed']);
    const wfh = useLoad<WFHState>(wfhFetcher, ['wfh_changed']);
    const week = useLoad<Hours>(hoursFetcher, ['session_changed']);
    const teamFetcher = useCallback(() => fetchTeamDay(teamId), [teamId]);
    const team = useLoad<TeamDay>(me.is_admin ? teamFetcher : null, ['wfh_changed']);

    // First name, capitalized (usernames are often lowercase).
    const word = (me.display_name || '').trim().split(/\s+/)[0] ?? '';
    const first = word.charAt(0).toUpperCase() + word.slice(1);

    // Attendance tile
    let attendance = `Not checked in · today ${hm(t.today)}`;
    if (me.session?.on_break) {
        attendance = `On break · today ${hm(t.today)}`;
    } else if (me.session) {
        attendance = `Working · today ${hm(t.today)}`;
    }

    // Tasks tile
    let tasksSummary: React.ReactNode = '…';
    if (!me.github_linked) {
        tasksSummary = 'GitHub not linked';
    } else if (tasks?.error) {
        tasksSummary = 'GitHub unavailable';
    } else if (tasks) {
        const picked = tasks.tasks.filter((x) => x.picked).length;
        const fresh = tasks.tasks.filter((x) => x.off_board).length;
        tasksSummary = tasks.tasks.length ? `${picked} today · ${tasks.tasks.length} open${fresh ? ` · ${fresh} 🆕` : ''}` : 'No open tasks';
    }

    // End-of-day tile
    let eod = 'Opens when you check out';
    let eodTone: TileProps['tone'] = '';
    if (me.eod.available && me.eod.submitted) {
        eod = `Sent · ${me.eod.sync_line?.replace(/^GitHub: /, 'GitHub ') ?? ''}`;
        eodTone = 'ok';
    } else if (me.eod.available) {
        eod = `Not sent yet · ${me.eod.day_label}`;
        eodTone = 'warn';
    }

    // WFH tile
    let wfhSummary = 'Request a day';
    if (wfh?.pending) {
        wfhSummary = `${wfh.pending} pending`;
    } else {
        const next = wfh?.requests.find((r) => r.status === 'approved' && r.end >= wfh.today);
        if (next) {
            wfhSummary = `Next: ${next.dates}`;
        }
    }

    // Hours tile
    let hoursSummary = '…';
    if (week) {
        hoursSummary = week.has_expected ? `${hm(week.all_seconds)} this week · ${Math.round((week.worked_until_yesterday / Math.max(1, week.expected_seconds)) * 100)}% of expected` : `${hm(week.all_seconds)} this week`;
    }

    // Team tile (admins)
    let teamSummary = '…';
    if (team) {
        const parts = [`${team.working + team.on_break} in`];
        if (team.eod_due > team.eod_sent) {
            parts.push(`${team.eod_due - team.eod_sent} EOD missing`);
        }
        if (team.wfh_pending) {
            parts.push(`${team.wfh_pending} WFH to decide`);
        }
        teamSummary = parts.join(' · ');
    }

    return (
        <div className='att-home'>
            <h2 className='att-greeting'>{`${greeting(new Date(now).getHours())}${first ? `, ${first}` : ''}`}</h2>
            <StatusBar {...props}/>
            {message}
            <div
                className='att-tiles'
                role='navigation'
                aria-label='Attendance pages'
            >
                <Tile
                    icon='⏱'
                    title='Attendance'
                    summary={attendance}
                    {...tile('attendance')}
                />
                {me.github_set_up && (
                    <Tile
                        icon='✅'
                        title='Tasks'
                        summary={tasksSummary}
                        {...tile('tasks')}
                    />
                )}
                <Tile
                    icon='📝'
                    title='End of day'
                    summary={eod}
                    tone={eodTone}
                    {...tile('eod')}
                />
                <Tile
                    icon='🏠'
                    title='Work from home'
                    summary={wfhSummary}
                    tone={wfh?.pending ? 'warn' : ''}
                    {...tile('wfh')}
                />
                <Tile
                    icon='📊'
                    title='Hours'
                    summary={hoursSummary}
                    {...tile('hours')}
                />
                <Tile
                    icon='⚙️'
                    title='My settings'
                    summary={me.github_linked ? `GitHub ✓ ${me.github_login ?? ''}` : 'GitHub not linked'}
                    {...tile('settings')}
                />
                {me.is_admin && (
                    <Tile
                        icon='👥'
                        title='Team'
                        summary={teamSummary}
                        tone={team && (team.wfh_pending || team.eod_due > team.eod_sent) ? 'warn' : ''}
                        {...tile('team')}
                    />
                )}
            </div>
        </div>
    );
}
