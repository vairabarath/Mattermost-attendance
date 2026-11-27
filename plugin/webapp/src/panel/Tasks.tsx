// "Your tasks" in the panel: the member's open tasks on the team's GitHub
// Project (same list as /att tasks), with a Pick button per task.

import {fetchTasks, pickTask} from 'api';
import type {TaskItem, TasksState} from 'api';
import React, {useCallback, useEffect, useState} from 'react';

import Markdown from './Markdown';
import {REFRESH_EVENT} from './refresh';

const SHOWN = 6;

const STATUS_CLASS: Record<string, string> = {
    'In progress': 'progress',
    'In review': 'review',
    Blocked: 'blocked',
    Todo: 'todo',
};

function dueLabel(due: string): string {
    const d = new Date(`${due}T00:00:00`);
    return Number.isNaN(d.getTime()) ? due : d.toLocaleDateString([], {weekday: 'short', day: 'numeric', month: 'short'});
}

function TaskRow({task, busy, onPick}: {task: TaskItem; busy: boolean; onPick: (t: TaskItem) => void}) {
    const repo = task.repo.split('/').pop();
    return (
        <li className='att-task'>
            <span
                className={`att-chip att-chip--${STATUS_CLASS[task.status] ?? 'none'}`}
                title='Status on the Project board'
            >{task.status || 'No status'}</span>
            <div className='att-task-main'>
                {/* Mattermost's ExternalLink component isn't available to plugins; rel=noopener noreferrer keeps the new tab safe. */}
                {/* eslint-disable-next-line @mattermost/use-external-link */}
                <a
                    className='att-task-title'
                    href={task.url}
                    target='_blank'
                    rel='noopener noreferrer'
                >
                    <span className='att-muted'>{`${repo}#${task.number} `}</span>
                    {task.title}
                </a>
                <div className='att-task-meta'>
                    {task.priority && <span>{task.priority}</span>}
                    {task.due && (
                        <span className={task.overdue ? 'att-overdue' : ''}>
                            {`${task.overdue ? '⚠️ overdue · ' : 'due '}${dueLabel(task.due)}`}
                        </span>
                    )}
                    {task.off_board && <span className='att-new'>{'🆕 added to the board'}</span>}
                </div>
            </div>
            {task.picked ? (
                <span
                    className='att-picked'
                    title='Picked for today: it will be in your end-of-day report'
                >{'✓ Today'}</span>
            ) : (
                <button
                    className='att-btn att-btn--small'
                    disabled={busy}
                    onClick={() => onPick(task)}
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
    const [message, setMessage] = useState('');
    const [showAll, setShowAll] = useState(false);

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
        setMessage('');
        setShowAll(false);
        load();
    }, [load]);

    useEffect(() => {
        const refresh = () => load();
        window.addEventListener(REFRESH_EVENT, refresh);
        return () => window.removeEventListener(REFRESH_EVENT, refresh);
    }, [load]);

    const pick = async (t: TaskItem) => {
        setBusy(t.url);
        try {
            const r = await pickTask(teamId, t.url);
            setData(r.tasks);
            setMessage(r.message);
            setFailed('');
        } catch (e) {
            setMessage('');
            setFailed((e as Error).message);
        } finally {
            setBusy('');
        }
    };

    if (data && !data.configured) {
        return null; // GitHub isn't used in this team
    }

    let body: React.ReactNode;
    if (!data && !failed) {
        body = <div className='att-muted'>{'Loading your tasks…'}</div>;
    } else if (data && !data.linked) {
        body = <div className='att-muted'>{'Your GitHub account isn’t linked yet. Send your GitHub username to an admin of this team.'}</div>;
    } else if (failed || data?.error) {
        body = (
            <div className='att-note'>
                {failed || data?.error}
                {' '}
                <button
                    className='att-link'
                    onClick={load}
                >{'Try again'}</button>
            </div>
        );
    } else if (data && data.tasks.length === 0) {
        body = <div className='att-muted'>{'No open tasks assigned to you. 🎉'}</div>;
    } else if (data) {
        const shown = showAll ? data.tasks : data.tasks.slice(0, SHOWN);
        body = (
            <>
                <ul className='att-tasks'>
                    {shown.map((t) => (
                        <TaskRow
                            key={t.url}
                            task={t}
                            busy={Boolean(busy)}
                            onPick={pick}
                        />
                    ))}
                </ul>
                {data.tasks.length > SHOWN && (
                    <button
                        className='att-link'
                        onClick={() => setShowAll(!showAll)}
                    >{showAll ? 'Show less' : `Show all ${data.tasks.length}`}</button>
                )}
            </>
        );
    }

    return (
        <section
            className='att-section'
            aria-label='Your tasks'
        >
            <h3 className='att-h'>
                {'Your tasks'}
                {data?.tasks.length ? <span className='att-count'>{data.tasks.length}</span> : null}
            </h3>
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
            {body}
            {data?.linked && data.tasks.length > 0 && (
                <p className='att-hint'>{'Pick what you’re working on today: it moves to In progress and gets its own row in your end-of-day report.'}</p>
            )}
        </section>
    );
}
