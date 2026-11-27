// "Tasks" in the panel: the member's open tasks on the team's GitHub
// Project (same list as /att tasks), grouped into "Picked for today" and
// "Other open tasks", with a Pick button on the others.

import {fetchTasks, pickTask} from 'api';
import type {TaskItem, TasksState} from 'api';
import React, {useCallback, useEffect, useRef, useState} from 'react';

import Markdown from './Markdown';
import {useRefresh} from './refresh';

const SHOWN = 5;
const NOTICE_MS = 6000;

const STATUS_KEY: Record<string, string> = {
    Todo: 'todo',
    'In progress': 'progress',
    'In review': 'review',
    Blocked: 'blocked',
};

// shortDate: "2026-11-25" → "25 Nov" (like the bot's messages).
export function shortDate(day: string): string {
    const d = new Date(`${day}T00:00:00`);
    return Number.isNaN(d.getTime()) ? day : d.toLocaleDateString('en-GB', {day: 'numeric', month: 'short'});
}

function TaskRow({task, busy, onPick}: {task: TaskItem; busy: boolean; onPick?: (t: TaskItem) => void}) {
    const key = STATUS_KEY[task.status] ?? 'none';
    const repo = task.repo.split('/').pop();
    return (
        <li className={`att-task${task.overdue ? ' att-task--overdue' : ''}`}>
            <span
                className={`att-task-dot att-task-dot--${key}`}
                aria-hidden='true'
            />
            <div className='att-task-main'>
                {/* Mattermost's ExternalLink component isn't available to plugins; rel=noopener noreferrer keeps the new tab safe. */}
                {/* eslint-disable-next-line @mattermost/use-external-link */}
                <a
                    className='att-task-title'
                    href={task.url}
                    target='_blank'
                    rel='noopener noreferrer'
                    title={`${task.title} — open on GitHub`}
                >{task.title}</a>
                <div className='att-task-meta'>
                    <span className={`att-pill att-pill--${key}`}>{task.status || 'No status'}</span>
                    <span>{`${repo} #${task.number}`}</span>
                    {task.priority && <span>{task.priority}</span>}
                    {task.due && task.overdue && <span className='att-pill att-pill--overdue'>{`Overdue · ${shortDate(task.due)}`}</span>}
                    {task.due && !task.overdue && <span>{`Due ${shortDate(task.due)}`}</span>}
                    {task.off_board && <span className='att-pill att-pill--new'>{'New on board'}</span>}
                </div>
            </div>
            {onPick && (
                <button
                    className='att-btn att-btn--small'
                    disabled={busy}
                    onClick={() => onPick(task)}
                    title='Work on this today: moves it to In progress and adds it to your end-of-day report'
                    aria-label={`Pick #${task.number} ${task.title} for today`}
                >{'Pick'}</button>
            )}
        </li>
    );
}

export default function Tasks({teamId}: {teamId: string}) {
    const [data, setData] = useState<TasksState | null>(null);
    const [failed, setFailed] = useState('');
    const [busy, setBusy] = useState('');
    const [notice, setNotice] = useState('');
    const [showAll, setShowAll] = useState(false);
    const noticeTimer = useRef<number>();

    const load = useCallback(async () => {
        try {
            setData(await fetchTasks(teamId));
            setFailed('');
        } catch (e) {
            setFailed((e as Error).message);
        }
    }, [teamId]);

    useEffect(() => {
        setData(null);
        setNotice('');
        setShowAll(false);
        load();
    }, [load]);

    useRefresh(['tasks_changed'], load);
    useEffect(() => () => window.clearTimeout(noticeTimer.current), []);

    const showNotice = (text: string) => {
        setNotice(text);
        window.clearTimeout(noticeTimer.current);
        noticeTimer.current = window.setTimeout(() => setNotice(''), NOTICE_MS);
    };

    const pick = async (t: TaskItem) => {
        setBusy(t.url);
        try {
            const r = await pickTask(teamId, t.url);
            setData(r.tasks);
            setFailed('');
            showNotice(r.message);
        } catch (e) {
            setNotice('');
            setFailed((e as Error).message);
        } finally {
            setBusy('');
        }
    };

    if (data && !data.configured) {
        return null; // GitHub isn't used in this team
    }

    const tasks = data?.tasks ?? [];
    const picked = tasks.filter((t) => t.picked);
    const others = tasks.filter((t) => !t.picked);
    const othersShown = showAll ? others : others.slice(0, SHOWN);

    let body: React.ReactNode;
    if (!data && !failed) {
        body = <div className='att-empty'>{'Loading your tasks…'}</div>;
    } else if (data && !data.linked) {
        body = <div className='att-empty'>{'Your GitHub account isn’t linked yet. Send your GitHub username to an admin of this team.'}</div>;
    } else if (failed || data?.error) {
        body = (
            <div className='att-empty'>
                {failed || data?.error}
                {' '}
                <button
                    className='att-link'
                    onClick={load}
                >{'Try again'}</button>
            </div>
        );
    } else if (tasks.length === 0) {
        body = <div className='att-empty'>{'No open tasks assigned to you. 🎉'}</div>;
    } else {
        body = (
            <>
                <div className='att-group'>
                    <div className='att-group-h'>{`Picked for today · ${picked.length}`}</div>
                    {picked.length === 0 ? (
                        <div className='att-empty att-empty--small'>{'Nothing picked yet. Pick what you’re working on below.'}</div>
                    ) : (
                        <ul className='att-tasks'>
                            {picked.map((t) => (
                                <TaskRow
                                    key={t.url}
                                    task={t}
                                    busy={false}
                                />
                            ))}
                        </ul>
                    )}
                </div>
                {others.length > 0 && (
                    <div className='att-group'>
                        <div className='att-group-h'>{`Other open tasks · ${others.length}`}</div>
                        <ul className='att-tasks'>
                            {othersShown.map((t) => (
                                <TaskRow
                                    key={t.url}
                                    task={t}
                                    busy={Boolean(busy)}
                                    onPick={pick}
                                />
                            ))}
                        </ul>
                        {others.length > SHOWN && (
                            <button
                                className='att-link att-more'
                                onClick={() => setShowAll(!showAll)}
                            >{showAll ? 'Show less' : `Show ${others.length - SHOWN} more`}</button>
                        )}
                    </div>
                )}
            </>
        );
    }

    return (
        <section
            className='att-card att-card--plain'
            aria-label='Your tasks'
        >
            <h3 className='att-h'>{'Tasks'}</h3>
            {notice && (
                <div
                    className='att-notice'
                    role='status'
                >
                    <Markdown text={notice}/>
                </div>
            )}
            {body}
        </section>
    );
}
