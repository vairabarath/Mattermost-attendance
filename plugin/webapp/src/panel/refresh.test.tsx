import {act, render} from '@testing-library/react';
import React from 'react';

import {ALL, REFRESH_EVENT, requestRefresh, useRefresh} from './refresh';

function Probe({events, onReload, enabled = true}: {events: string[]; onReload: () => void; enabled?: boolean}) {
    useRefresh(events, onReload, enabled);
    return null;
}

beforeEach(() => jest.useFakeTimers());
afterEach(() => jest.useRealTimers());

test('reloads only for its events (and ALL), once per burst', () => {
    const tasks = jest.fn();
    const status = jest.fn();
    render(
        <>
            <Probe
                events={['tasks_changed']}
                onReload={tasks}
            />
            <Probe
                events={['session_changed', 'wfh_changed']}
                onReload={status}
            />
        </>,
    );

    act(() => {
        requestRefresh('session_changed');
        requestRefresh('session_changed');
        requestRefresh('wfh_changed');
        jest.advanceTimersByTime(300);
    });
    expect(status).toHaveBeenCalledTimes(1); // a burst → one reload
    expect(tasks).not.toHaveBeenCalled(); // not its event

    act(() => {
        requestRefresh('tasks_changed');
        jest.advanceTimersByTime(300);
    });
    expect(tasks).toHaveBeenCalledTimes(1);
    expect(status).toHaveBeenCalledTimes(1);

    act(() => {
        requestRefresh(ALL);
        window.dispatchEvent(new Event(REFRESH_EVENT)); // no detail = everything
        jest.advanceTimersByTime(300);
    });
    expect(tasks).toHaveBeenCalledTimes(2);
    expect(status).toHaveBeenCalledTimes(2);
});

test('disabled (e.g. a form is open): no reloads', () => {
    const reload = jest.fn();
    render(
        <Probe
            events={['eod_changed']}
            onReload={reload}
            enabled={false}
        />,
    );
    act(() => {
        requestRefresh('eod_changed');
        jest.advanceTimersByTime(300);
    });
    expect(reload).not.toHaveBeenCalled();
});
