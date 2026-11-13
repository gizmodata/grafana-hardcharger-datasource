// Jest setup provided by Grafana scaffolding
import './.config/jest-setup';

// The scaffold gives React's scheduler Node's MessageChannel, whose open port keeps a Jest process
// from exiting (a run of one file then hangs after its tests pass). React only needs a message to
// arrive a moment later, which a timer does without leaving anything open.
class TimerMessageChannel {
  constructor() {
    this.port1 = { onmessage: null };
    this.port2 = {
      postMessage: (data) => {
        setTimeout(() => {
          if (typeof this.port1.onmessage === 'function') {
            this.port1.onmessage({ data });
          }
        }, 0);
      },
    };
  }
}
Object.assign(global, { MessageChannel: TimerMessageChannel });
