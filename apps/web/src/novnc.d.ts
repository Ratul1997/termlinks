declare module "@novnc/novnc" {
  type Credentials = {
    username?: string;
    password?: string;
    target?: string;
  };

  type RFBOptions = {
    shared?: boolean;
    credentials?: Credentials;
    repeaterID?: string;
    wsProtocols?: string[];
  };

  export default class RFB extends EventTarget {
    constructor(target: HTMLElement, urlOrChannel: object | string, options?: RFBOptions);
    viewOnly: boolean;
    scaleViewport: boolean;
    resizeSession: boolean;
    clipViewport: boolean;
    dragViewport: boolean;
    qualityLevel: number;
    compressionLevel: number;
    focusOnClick: boolean;
    disconnect(): void;
    focus(options?: FocusOptions): void;
    blur(): void;
    sendCredentials(credentials: Credentials): void;
    clipboardPasteFrom(text: string): void;
    sendCtrlAltDel(): void;
    sendKey(keysym: number, code: string | null, down?: boolean): void;
  }
}
