export namespace core {
	
	export class BlockedItem {
	    id: string;
	    path: string;
	    reason: string;
	
	    static createFrom(source: any = {}) {
	        return new BlockedItem(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.path = source["path"];
	        this.reason = source["reason"];
	    }
	}
	export class Item {
	    id: string;
	    ruleId: string;
	    name: string;
	    path: string;
	    projectPath?: string;
	    category: string;
	    tier: string;
	    bytes: number;
	    files: number;
	    modifiedAt?: string;
	    signals?: string[];
	    explanation: string;
	    rebuild: string;
	    native?: string;
	    cost: string;
	    clean: string;
	
	    static createFrom(source: any = {}) {
	        return new Item(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.ruleId = source["ruleId"];
	        this.name = source["name"];
	        this.path = source["path"];
	        this.projectPath = source["projectPath"];
	        this.category = source["category"];
	        this.tier = source["tier"];
	        this.bytes = source["bytes"];
	        this.files = source["files"];
	        this.modifiedAt = source["modifiedAt"];
	        this.signals = source["signals"];
	        this.explanation = source["explanation"];
	        this.rebuild = source["rebuild"];
	        this.native = source["native"];
	        this.cost = source["cost"];
	        this.clean = source["clean"];
	    }
	}
	export class CleanupPreview {
	    items: Item[];
	    blocked: BlockedItem[];
	    totalBytes: number;
	    confirmationToken: string;
	    mode: string;
	    immediateReclaim: boolean;
	
	    static createFrom(source: any = {}) {
	        return new CleanupPreview(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.items = this.convertValues(source["items"], Item);
	        this.blocked = this.convertValues(source["blocked"], BlockedItem);
	        this.totalBytes = source["totalBytes"];
	        this.confirmationToken = source["confirmationToken"];
	        this.mode = source["mode"];
	        this.immediateReclaim = source["immediateReclaim"];
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
	export class CleanupRequest {
	    ids: string[];
	    mode: string;
	    acknowledgeRisk: boolean;
	    confirmation: string;
	
	    static createFrom(source: any = {}) {
	        return new CleanupRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ids = source["ids"];
	        this.mode = source["mode"];
	        this.acknowledgeRisk = source["acknowledgeRisk"];
	        this.confirmation = source["confirmation"];
	    }
	}
	export class CleanupResult {
	    mode: string;
	    completed: Item[];
	    failed: BlockedItem[];
	    estimatedBytes: number;
	    actualBytes: number;
	    quarantineId?: string;
	    finishedAt: string;
	
	    static createFrom(source: any = {}) {
	        return new CleanupResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.mode = source["mode"];
	        this.completed = this.convertValues(source["completed"], Item);
	        this.failed = this.convertValues(source["failed"], BlockedItem);
	        this.estimatedBytes = source["estimatedBytes"];
	        this.actualBytes = source["actualBytes"];
	        this.quarantineId = source["quarantineId"];
	        this.finishedAt = source["finishedAt"];
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
	export class Group {
	    id: string;
	    title: string;
	    description: string;
	    tier: string;
	    category: string;
	    bytes: number;
	    count: number;
	    itemIds: string[];
	
	    static createFrom(source: any = {}) {
	        return new Group(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.title = source["title"];
	        this.description = source["description"];
	        this.tier = source["tier"];
	        this.category = source["category"];
	        this.bytes = source["bytes"];
	        this.count = source["count"];
	        this.itemIds = source["itemIds"];
	    }
	}
	
	export class TierSummary {
	    bytes: number;
	    count: number;
	
	    static createFrom(source: any = {}) {
	        return new TierSummary(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.bytes = source["bytes"];
	        this.count = source["count"];
	    }
	}
	export class Result {
	    generatedAt: string;
	    roots: string[];
	    items: Item[];
	    summary: Record<string, TierSummary>;
	    groups: Group[];
	    durationMs: number;
	
	    static createFrom(source: any = {}) {
	        return new Result(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.generatedAt = source["generatedAt"];
	        this.roots = source["roots"];
	        this.items = this.convertValues(source["items"], Item);
	        this.summary = this.convertValues(source["summary"], TierSummary, true);
	        this.groups = this.convertValues(source["groups"], Group);
	        this.durationMs = source["durationMs"];
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
	export class Rule {
	    id: string;
	    name: string;
	    kind: string;
	    markers?: string[];
	    targets?: string[];
	    tier: string;
	    category: string;
	    rebuild: string;
	    native?: string;
	    cost: string;
	
	    static createFrom(source: any = {}) {
	        return new Rule(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.kind = source["kind"];
	        this.markers = source["markers"];
	        this.targets = source["targets"];
	        this.tier = source["tier"];
	        this.category = source["category"];
	        this.rebuild = source["rebuild"];
	        this.native = source["native"];
	        this.cost = source["cost"];
	    }
	}

}

