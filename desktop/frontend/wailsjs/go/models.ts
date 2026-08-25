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
	export class ServiceView {
	    id: string;
	    title: string;
	    profile: string;
	    command: string;
	
	    static createFrom(source: any = {}) {
	        return new ServiceView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.title = source["title"];
	        this.profile = source["profile"];
	        this.command = source["command"];
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

