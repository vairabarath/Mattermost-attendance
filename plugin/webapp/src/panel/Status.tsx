// Loading and failure states shared by the panel's pages: a loading line
// screen readers hear once, and an error they hear at once, with a retry.

import React from 'react';

export function Loading({text = 'Loading…'}: {text?: string}) {
    return (
        <div
            className='att-empty'
            role='status'
        >{text}</div>
    );
}

export function Failed({message, onRetry}: {message: string; onRetry: () => void}) {
    return (
        <div
            className='att-empty att-empty--error'
            role='alert'
        >
            {message || 'Something went wrong.'}
            {' '}
            <button
                className='att-link'
                onClick={onRetry}
            >{'Try again'}</button>
        </div>
    );
}

// LoadState is Failed if there's an error, otherwise Loading.
export function LoadState({failed, onRetry, text}: {failed: string; onRetry: () => void; text?: string}) {
    return failed ? (
        <Failed
            message={failed}
            onRetry={onRetry}
        />
    ) : <Loading text={text}/>;
}
