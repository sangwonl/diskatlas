export namespace core {
	
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
	
	    static createFrom(source: any = {}) {
	        return new Result(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.generatedAt = source["generatedAt"];
	        this.roots = source["roots"];
	        this.items = this.convertValues(source["items"], Item);
	        this.summary = this.convertValues(source["summary"], TierSummary, true);
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

