// The Attendance panel (right-hand side): a home screen with tiles, and a
// page per tile (← Home to go back). It follows the Mattermost team the user
// is looking at and refreshes itself on live events (see refresh.ts).

import {act, fetchMe} from 'api';
import type {Me, SessionAction} from 'api';
import React, {useCallback, useEffect, useRef, useState} from 'react';
import {useSelector} from 'react-redux';

import type {GlobalState} from '@mattermost/types/store';

import {getCurrentTeamId} from 'mattermost-redux/selectors/entities/teams';

import EODSection from './EODSection';
import Home from './Home';
import type {Page} from './Home';
import HoursPage from './HoursPage';
import Markdown from './Markdown';
import {ALL, requestRefresh, useRefresh} from './refresh';
import SettingsPage from './SettingsPage';
import StatusCard from './StatusCard';
import Tasks from './Tasks';
import TeamPage from './TeamPage';
import WFHPage from './WFHPage';

import './panel.css';

const POLL_MS = 5 * 60 * 1000;
const PAGE_KEY = 'attendance.page';

const TITLES: Record<Exclude<Page, 'home'>, string> = {
    attendance: '⏱ Attendance',
    tasks: '✅ Tasks',
    eod: '📝 End of day',
    wfh: '🏠 Work from home',
    hours: '📊 Hours',
    settings: '⚙️ My settings',
    team: '👥 Team',
};

// The page survives closing and reopening the panel while Mattermost is open.
function savedPage(): Page {
    try {
        const p = window.sessionStorage.getItem(PAGE_KEY) as Page | null;
        return p && (p === 'home' || p in TITLES) ? p : 'home';
    } catch {
        return 'home';
    }
}

function savePage(p: Page) {
    try {
        window.sessionStorage.setItem(PAGE_KEY, p);
    } catch {
        // private mode etc.: just don't remember
    }
}

export default function Panel() {
    const teamId = useSelector((state: GlobalState) => getCurrentTeamId(state));
    const [me, setMe] = useState<Me | null>(null);
    const [fetchedAt, setFetchedAt] = useState(0);
    const [error, setError] = useState('');
    const [loading, setLoading] = useState(true);
    const [busy, setBusy] = useState<SessionAction | ''>('');
    const [message, setMessage] = useState('');
    const [page, setPage] = useState<Page>(savedPage);
    const [eodOpen, setEodOpen] = useState(false);

    // Keyboard and screen-reader users: opening a page moves focus to its
    // title; ← Home puts it back on the tile they came from.
    const [cameFrom, setCameFrom] = useState<Page | null>(null);
    const titleRef = useRef<HTMLHeadingElement>(null);
    const navigated = useRef(false);

    const open = (p: Page) => {
        navigated.current = true;
        setCameFrom(p === 'home' ? page : null);
        setPage(p);
        savePage(p);
        setMessage('');
        setEodOpen(false);
    };

    const show = (m: Me) => {
        setMe(m);
        setFetchedAt(Date.now());
        setError('');
    };

    const load = useCallback(async () => {
        if (!teamId) {
            return;
        }
        try {
            show(await fetchMe(teamId));
        } catch (e) {
            setMe(null);
            setError((e as Error).message);
        } finally {
            setLoading(false);
        }
    }, [teamId]);

    useEffect(() => {
        setLoading(true);
        setMessage('');
        setEodOpen(false);
        load();
    }, [load]);

    // A change made elsewhere (chat command, another window, a job): the
    // last reply here is out of date. (eod_changed: the Home tile.)
    useRefresh(['session_changed', 'wfh_changed', 'eod_changed'], () => {
        setMessage('');
        load();
    });

    // Window shown again, and a slow safety poll: everything reloads.
    useEffect(() => {
        const onVisible = () => document.visibilityState === 'visible' && requestRefresh(ALL);
        document.addEventListener('visibilitychange', onVisible);
        const id = window.setInterval(() => requestRefresh(ALL), POLL_MS);
        return () => {
            document.removeEventListener('visibilitychange', onVisible);
            window.clearInterval(id);
        };
    }, []);

    // Checking in or out (here, in chat or in another window) changes the
    // end-of-day report; a break doesn't. Tell the report only then.
    const wasIn = useRef<boolean | null>(null);
    const isIn = me ? Boolean(me.session) : null;
    useEffect(() => {
        if (isIn === null) {
            return;
        }
        if (wasIn.current !== null && wasIn.current !== isIn) {
            requestRefresh('eod_changed', teamId);
        }
        wasIn.current = isIn;
    }, [isIn, teamId]);

    useEffect(() => {
        if (navigated.current && page !== 'home') {
            titleRef.current?.focus();
        }
    }, [page]);

    const run = async (action: SessionAction, text = '') => {
        setBusy(action);
        try {
            const r = await act(teamId, action, text);
            show(r.me);
            setMessage(r.message);
            if (action === 'out') {
                // Like the dialog after /att out: straight to the report.
                navigated.current = true;
                setPage('eod');
                savePage('eod');
                setEodOpen(true);
            }
        } catch (e) {
            setMessage('');
            setError((e as Error).message);
        } finally {
            setBusy('');
        }
    };

    if (loading && !me) {
        return <div className='att-panel'><div className='att-muted'>{'Loading…'}</div></div>;
    }
    if (!me) {
        return (
            <div className='att-panel'>
                <div
                    className='att-error'
                    role='alert'
                >{error || 'Couldn’t load your attendance.'}</div>
                <button
                    className='att-btn'
                    onClick={load}
                >{'Try again'}</button>
            </div>
        );
    }

    const session = {me, fetchedAt, busy, run};
    const notes = (
        <>
            {error && (
                <div
                    className='att-error'
                    role='alert'
                >{error}</div>
            )}
            {message && (
                <div
                    className='att-message'
                    role='status'
                >
                    <button
                        className='att-close'
                        aria-label='Dismiss'
                        onClick={() => setMessage('')}
                    >{'×'}</button>
                    <Markdown text={message}/>
                </div>
            )}
        </>
    );

    // Tasks only exist in teams with GitHub, Team only for admins (the bot
    // refuses members anyway): fall back to Home otherwise.
    let current: Page = page;
    if ((page === 'tasks' && !me.github_set_up) || (page === 'team' && !me.is_admin)) {
        current = 'home';
    }

    let body: React.ReactNode;
    switch (current) {
    case 'attendance':
        body = (
            <>
                <StatusCard {...session}/>
                {notes}
            </>
        );
        break;
    case 'tasks':
        body = (
            <Tasks
                teamId={me.team.id}
                showTitle={false}
            />
        );
        break;
    case 'eod':
        body = (
            <>
                {notes}
                <EODSection
                    teamId={me.team.id}
                    open={eodOpen}
                    onOpen={() => setEodOpen(true)}
                    onClose={() => setEodOpen(false)}
                />
            </>
        );
        break;
    case 'wfh':
        body = <WFHPage teamId={me.team.id}/>;
        break;
    case 'hours':
        body = <HoursPage teamId={me.team.id}/>;
        break;
    case 'settings':
        body = <SettingsPage teamId={me.team.id}/>;
        break;
    case 'team':
        body = <TeamPage teamId={me.team.id}/>;
        break;
    default:
        body = (
            <Home
                {...session}
                message={notes}
                onOpen={open}
                focusTile={navigated.current ? cameFrom : null}
            />
        );
    }

    return (
        <div className='att-panel'>
            <div className='att-top'>
                {current === 'home' ? (
                    <span className='att-team'>{me.team.name}</span>
                ) : (
                    <>
                        <button
                            className='att-back'
                            onClick={() => open('home')}
                        >{'← Home'}</button>
                        <h2
                            className='att-page-title'
                            ref={titleRef}
                            tabIndex={-1}
                        >{TITLES[current as Exclude<Page, 'home'>]}</h2>
                        <span className='att-team att-team--small'>{me.team.name}</span>
                    </>
                )}
            </div>
            {body}
        </div>
    );
}
