export namespace main {
	
	export class DeviceAuthView {
	    userCode: string;
	    verificationUri: string;
	    verificationUriComplete: string;
	
	    static createFrom(source: any = {}) {
	        return new DeviceAuthView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.userCode = source["userCode"];
	        this.verificationUri = source["verificationUri"];
	        this.verificationUriComplete = source["verificationUriComplete"];
	    }
	}
	export class SSOProfileView {
	    name: string;
	    accountId: string;
	    roleName: string;
	    loggedIn: boolean;
	    expiresAt: string;
	
	    static createFrom(source: any = {}) {
	        return new SSOProfileView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.accountId = source["accountId"];
	        this.roleName = source["roleName"];
	        this.loggedIn = source["loggedIn"];
	        this.expiresAt = source["expiresAt"];
	    }
	}
	export class SSOSummary {
	    expired: number;
	    soonestSeconds: number;
	    known: boolean;
	
	    static createFrom(source: any = {}) {
	        return new SSOSummary(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.expired = source["expired"];
	        this.soonestSeconds = source["soonestSeconds"];
	        this.known = source["known"];
	    }
	}
	export class tunnelLive {
	    status: string;
	    since: string;
	    health: string;
	    latencyMs: number;
	    healthErr: string;
	    verifiedAt: string;
	    verifiedOk: boolean;
	
	    static createFrom(source: any = {}) {
	        return new tunnelLive(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.status = source["status"];
	        this.since = source["since"];
	        this.health = source["health"];
	        this.latencyMs = source["latencyMs"];
	        this.healthErr = source["healthErr"];
	        this.verifiedAt = source["verifiedAt"];
	        this.verifiedOk = source["verifiedOk"];
	    }
	}
	export class ServiceView {
	    id: string;
	    proyecto: string;
	    title: string;
	    profile: string;
	    localPort: number;
	    connectionStringMac: string;
	    connectionStringWindows: string;
	    command: string;
	    live: tunnelLive;
	
	    static createFrom(source: any = {}) {
	        return new ServiceView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.proyecto = source["proyecto"];
	        this.title = source["title"];
	        this.profile = source["profile"];
	        this.localPort = source["localPort"];
	        this.connectionStringMac = source["connectionStringMac"];
	        this.connectionStringWindows = source["connectionStringWindows"];
	        this.command = source["command"];
	        this.live = this.convertValues(source["live"], tunnelLive);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class VerifyItem {
	    id: string;
	    title: string;
	    state: string;
	    note: string;
	
	    static createFrom(source: any = {}) {
	        return new VerifyItem(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.title = source["title"];
	        this.state = source["state"];
	        this.note = source["note"];
	    }
	}
	export class VerifyUpdate {
	    items: VerifyItem[];
	    parallel: boolean;
	    done: boolean;
	    elapsedMs: number;
	    disabled: number;
	
	    static createFrom(source: any = {}) {
	        return new VerifyUpdate(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.items = this.convertValues(source["items"], VerifyItem);
	        this.parallel = source["parallel"];
	        this.done = source["done"];
	        this.elapsedMs = source["elapsedMs"];
	        this.disabled = source["disabled"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace ssologin {
	
	export class Role {
	    RoleName: string;
	    AccountID: string;
	
	    static createFrom(source: any = {}) {
	        return new Role(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.RoleName = source["RoleName"];
	        this.AccountID = source["AccountID"];
	    }
	}

}

