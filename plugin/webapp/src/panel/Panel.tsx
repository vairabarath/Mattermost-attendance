// The Attendance panel (right-hand side). Phase 2: status card with a live
// timer, check in / break / back / check out. It follows the Mattermost team
// the user is looking at, and refreshes when the bot says something changed.

import {act, fetchMe} from 'api';
import type {Me, SessionAction} from 'api';
import {clock, hm, timeOfDay} from 'format';
import React, {useCallback, useEffect, useRef, useState} from 'react';
import {useSelector} from 'react-redux';

import type {GlobalState} from '@mattermost/types/store';

import {getCurrentTeamId} from 'mattermost-redux/selectors/entities/teams';

import EODSection from './EODSection';
import Markdown from './Markdown';
import {ALL, requestRefresh, useRefresh} from './refresh';
import Tasks from './Tasks';

import './panel.css';

const POLL_MS = 5 * 60 * 1000;

// useNow ticks every second while active.
function useNow(active: boolean): number {
    const [now, setNow] = useState(() => Date.now());
    useEffect(() => {
        if (!active) {
            return undefined;
        }
        setNow(Date.now());
        const id = window.setInterval(() => setNow(Date.now()), 1000);
        return () => window.clearInterval(id);
    }, [active]);
    return now;
}

export default function Panel() {
    const teamId = useSelector((state: GlobalState) => getCurrentTeamId(state));
    const [me, setMe] = useState<Me | null>(null);
    const [fetchedAt, setFetchedAt] = useState(0);
    const [error, setError] = useState('');
    const [loading, setLoading] = useState(true);
    const [busy, setBusy] = useState<SessionAction | ''>('');
    const [message, setMessage] = useState('');
    const [confirmOut, setConfirmOut] = useState(false);
    const [showNote, setShowNote] = useState(false);
    const [note, setNote] = useState('');
    const [eodOpen, setEodOpen] = useState(false);

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
        setConfirmOut(false);
        setEodOpen(false);
        load();
    }, [load]);

    // A change made elsewhere (chat command, another window, a job): the
    // last reply here is out of date.
    useRefresh(['session_changed', 'wfh_changed'], () => {
        setMessage('');
        setConfirmOut(false);
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

    // Checking in or out (here, in chat or in another window) changes what
    // the end-of-day card shows; a break doesn't. Tell the card only then.
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

    const session = me?.session ?? null;
    const ticking = Boolean(session && !session.on_break);
    const now = useNow(Boolean(session));
    const sinceFetch = fetchedAt ? Math.max(0, (now - fetchedAt) / 1000) : 0;

    const run = async (action: SessionAction, text = '') => {
        setBusy(action);
        setConfirmOut(false);
        try {
            const r = await act(teamId, action, text);
            show(r.me);
            setMessage(r.message);
            setNote('');
            setShowNote(false);
            if (action === 'out') {
                setEodOpen(true); // like the dialog after /att out
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

    const elsewhere = session && session.team.id !== me.team.id;
    const workSeconds = session ? session.net_seconds + (ticking ? sinceFetch : 0) : 0;
    const todaySeconds = me.today_team_seconds + (ticking && !elsewhere ? sinceFetch : 0);
    const allSeconds = me.today_all_seconds + (ticking ? sinceFetch : 0);
    const breakSeconds = session?.on_break && session.break_since ? (now - Date.parse(session.break_since)) / 1000 : 0;
    const modeLabel = session?.mode === 'wfh' ? '🏠 Working from home' : '🏢 Office';

    let state = 'off';
    if (session?.on_break) {
        state = 'break';
    } else if (session) {
        state = 'on';
    }

    return (
        <div className='att-panel'>
            <div className='att-team'>{me.team.name}</div>
            {!eodOpen && (<>

                <section
                    className={`att-card att-card--${state}`}
                    aria-label='Attendance status'
                >
                    {!session && (
                        <>
                            <div className='att-status'>
                                <span className='att-dot att-dot--off'/>
                                {'Not checked in'}
                            </div>
                            {showNote ? (
                                <textarea
                                    className='att-input'
                                    placeholder='Note (optional), e.g. “standup at 10”'
                                    maxLength={500}
                                    value={note}
                                    onChange={(e) => setNote(e.target.value)}
                                    aria-label='Check-in note'
                                />
                            ) : (
                                <button
                                    className='att-link'
                                    onClick={() => setShowNote(true)}
                                >{'+ Add a note'}</button>
                            )}
                            <button
                                className='att-btn att-btn--primary att-btn--wide'
                                disabled={Boolean(busy)}
                                onClick={() => run('in', note)}
                            >{busy === 'in' ? 'Checking in…' : '▶ Check in'}</button>
                        </>
                    )}

                    {session && (
                        <>
                            <div className='att-status'>
                                <span className={`att-dot att-dot--${state}`}/>
                                {session.on_break ? 'On break' : 'Working'}
                                {elsewhere && <span className='att-muted'>{` · in ${session.team.name}`}</span>}
                            </div>
                            <div
                                className='att-timer'
                                aria-label='Time worked this session'
                            >{clock(workSeconds)}</div>
                            <div className='att-muted'>
                                {`since ${timeOfDay(session.check_in)} · ${modeLabel}`}
                                {session.on_break && ` · break ${hm(breakSeconds)}`}
                            </div>
                            <div className='att-actions'>
                                {session.on_break ? (
                                    <button
                                        className='att-btn att-btn--primary'
                                        disabled={Boolean(busy)}
                                        onClick={() => run('back')}
                                    >{busy === 'back' ? '…' : '▶ Back'}</button>
                                ) : (
                                    <button
                                        className='att-btn'
                                        disabled={Boolean(busy)}
                                        onClick={() => run('break')}
                                    >{busy === 'break' ? '…' : '☕ Break'}</button>
                                )}
                                {confirmOut ? (
                                    <span className='att-confirm'>
                                        <button
                                            className='att-btn att-btn--danger'
                                            disabled={Boolean(busy)}
                                            onClick={() => run('out')}
                                        >{busy === 'out' ? 'Checking out…' : 'Yes, check out'}</button>
                                        <button
                                            className='att-link'
                                            onClick={() => setConfirmOut(false)}
                                        >{'Cancel'}</button>
                                    </span>
                                ) : (
                                    <button
                                        className='att-btn att-btn--danger'
                                        disabled={Boolean(busy)}
                                        onClick={() => setConfirmOut(true)}
                                    >{'🔴 Check out'}</button>
                                )}
                            </div>
                        </>
                    )}
                    <div
                        className='att-card-foot'
                        aria-label='Today'
                    >
                        <span>
                            <span className='att-muted'>{'Today '}</span>
                            <strong>{hm(todaySeconds)}</strong>
                        </span>
                        {allSeconds !== todaySeconds && (
                            <span>
                                <span className='att-muted'>{'All orgs '}</span>
                                <strong>{hm(allSeconds)}</strong>
                            </span>
                        )}
                    </div>
                </section>

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

            </>)}

            <EODSection
                teamId={me.team.id}
                open={eodOpen}
                onOpen={() => setEodOpen(true)}
                onClose={() => setEodOpen(false)}
            />

            {!eodOpen && <Tasks teamId={me.team.id}/>}
        </div>
    );
}
